package broadcasts

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/push"
)

// Target is one device a broadcast is sent to.
type Target struct {
	TokenID string // device_tokens.id — the keyset cursor
	Token   string
	Lang    string // customers.lang
}

// BatchResult is what one batch changed, persisted atomically with the
// new cursor.
type BatchResult struct {
	Sent, Failed, InvalidRemoved int
	Cursor                       string // last TokenID of the batch
}

// workerStore is the persistence the Worker needs (Repo in production).
type workerStore interface {
	// NextPending returns the oldest queued/sending broadcast, or nil.
	NextPending(ctx context.Context) (*Broadcast, error)
	// MarkSending moves a queued broadcast to sending and records its
	// audience size; a no-op for one already sending (resumed).
	MarkSending(ctx context.Context, id string) error
	// NextTargets returns up to limit audience devices after cursor
	// (nil = from the start), ordered by device_tokens.id.
	NextTargets(ctx context.Context, cursor *string, limit int) ([]Target, error)
	// RecordBatch deletes invalid tokens and adds the batch's counters
	// and cursor to the broadcast, in one transaction.
	RecordBatch(ctx context.Context, id string, res BatchResult, invalidTokens []string) error
	// Finish marks the broadcast done (errMsg == "") or failed.
	Finish(ctx context.Context, id, errMsg string) error
}

// Worker defaults.
const (
	DefaultBatchSize    = 200
	DefaultConcurrency  = 8
	DefaultPollInterval = 30 * time.Second
	// batchTimeout bounds one batch's sends. They run on a context that a
	// shutdown does NOT cancel, so a stop request lets the in-flight batch
	// finish and be recorded instead of losing track of it.
	batchTimeout = 2 * time.Minute
	// maxAttemptErrors: after this many consecutive store errors on one
	// broadcast the worker marks it failed instead of retrying forever.
	maxAttemptErrors = 5
)

// WorkerConfig tunes a Worker; zero values use the defaults.
type WorkerConfig struct {
	BatchSize    int
	Concurrency  int
	PollInterval time.Duration
}

// Worker sends queued broadcasts in the background. One per process (the
// deployment runs a single backend instance).
type Worker struct {
	store  workerStore
	sender push.Sender
	cfg    WorkerConfig
	wake   chan struct{}

	errMu  sync.Mutex
	errors map[string]int // consecutive store errors per broadcast
}

func NewWorker(store workerStore, sender push.Sender, cfg WorkerConfig) *Worker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	return &Worker{store: store, sender: sender, cfg: cfg, wake: make(chan struct{}, 1), errors: map[string]int{}}
}

// defaultWorker is the process's worker, so the admin handler can nudge it
// right after queuing a broadcast without holding a reference to it.
var (
	defaultMu     sync.Mutex
	defaultWorker *Worker
)

// Nudge wakes the process's running worker (if any) so a just-queued
// broadcast starts now rather than at the next poll.
func Nudge() {
	defaultMu.Lock()
	w := defaultWorker
	defaultMu.Unlock()
	if w != nil {
		w.Wake()
	}
}

// Wake makes Run check for work immediately.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start runs the worker on a goroutine until ctx is cancelled and
// registers it for Nudge. The returned channel closes once it has
// stopped (after finishing and recording the batch in flight).
func (w *Worker) Start(ctx context.Context) <-chan struct{} {
	defaultMu.Lock()
	defaultWorker = w
	defaultMu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			defaultMu.Lock()
			if defaultWorker == w {
				defaultWorker = nil
			}
			defaultMu.Unlock()
		}()
		w.run(ctx)
	}()
	return done
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

// drain sends pending broadcasts one after another until none is left,
// a store error needs a later retry, or ctx is cancelled.
func (w *Worker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		b, err := w.store.NextPending(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("broadcasts: loading pending broadcast failed", "err", err)
			}
			return
		}
		if b == nil {
			return
		}
		if !w.process(ctx, b) {
			return
		}
	}
}

// process sends b batch by batch. It returns true when b reached a final
// status (so drain moves on) and false when it should be retried later
// (store error, or shutdown).
func (w *Worker) process(ctx context.Context, b *Broadcast) bool {
	logger := slog.With("broadcast_id", b.ID)
	if b.Status == StatusQueued {
		if err := w.store.MarkSending(ctx, b.ID); err != nil {
			return w.storeError(ctx, b.ID, "starting", err)
		}
		logger.Info("broadcasts: sending started")
	} else {
		logger.Info("broadcasts: resuming", "sent_so_far", b.Sent)
	}

	msgs := messagesFor(b)
	cursor := b.CursorTokenID
	for {
		if ctx.Err() != nil {
			logger.Info("broadcasts: paused for shutdown, will resume on restart")
			return false
		}
		targets, err := w.store.NextTargets(ctx, cursor, w.cfg.BatchSize)
		if err != nil {
			return w.storeError(ctx, b.ID, "loading targets", err)
		}
		if len(targets) == 0 {
			if err := w.store.Finish(ctx, b.ID, ""); err != nil {
				return w.storeError(ctx, b.ID, "finishing", err)
			}
			w.clearErrors(b.ID)
			logger.Info("broadcasts: done")
			return true
		}

		res, invalid := w.sendBatch(ctx, targets, msgs)
		// Recorded even if ctx was cancelled meanwhile: the sends happened.
		recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = w.store.RecordBatch(recCtx, b.ID, res, invalid)
		cancel()
		if err != nil {
			return w.storeError(ctx, b.ID, "recording batch", err)
		}
		w.clearErrors(b.ID)
		c := res.Cursor
		cursor = &c
	}
}

// sendBatch pushes msgs to targets with bounded concurrency. The sends use
// a context detached from ctx (bounded by batchTimeout) so shutdown lets
// the batch complete.
func (w *Worker) sendBatch(ctx context.Context, targets []Target, msgs map[string]push.Message) (BatchResult, []string) {
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), batchTimeout)
	defer cancel()

	var (
		mu      sync.Mutex
		res     BatchResult
		invalid []string
		wg      sync.WaitGroup
		sem     = make(chan struct{}, w.cfg.Concurrency)
	)
	for _, t := range targets {
		msg, ok := msgs[t.Lang]
		if !ok {
			msg = msgs[i18n.DefaultLang]
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t Target, msg push.Message) {
			defer wg.Done()
			defer func() { <-sem }()
			err := w.sender.Send(sendCtx, t.Token, msg)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				res.Sent++
			case errors.Is(err, push.ErrInvalidToken):
				res.InvalidRemoved++
				invalid = append(invalid, t.Token)
			default:
				res.Failed++
				slog.Warn("broadcasts: push send failed", "err", err)
			}
		}(t, msg)
	}
	wg.Wait()
	res.Cursor = targets[len(targets)-1].TokenID
	return res, invalid
}

// storeError logs a store failure and, after maxAttemptErrors in a row for
// the same broadcast, gives up on it (status failed) so one broken row
// can't block the queue forever. Returns false = retry later.
func (w *Worker) storeError(ctx context.Context, id, step string, err error) bool {
	if ctx.Err() != nil {
		return false // shutting down; not the broadcast's fault
	}
	w.errMu.Lock()
	w.errors[id]++
	n := w.errors[id]
	w.errMu.Unlock()
	slog.Error("broadcasts: store error", "broadcast_id", id, "step", step, "attempt", n, "err", err)
	if n < maxAttemptErrors {
		return false
	}
	if ferr := w.store.Finish(ctx, id, step+": "+err.Error()); ferr != nil {
		slog.Error("broadcasts: marking broadcast failed also failed", "broadcast_id", id, "err", ferr)
		return false
	}
	w.clearErrors(id)
	return true
}

func (w *Worker) clearErrors(id string) {
	w.errMu.Lock()
	delete(w.errors, id)
	w.errMu.Unlock()
}

// messagesFor builds the per-language push for b: Kyrgyz customers get
// the KY text, falling back field by field to RU when KY was left empty.
func messagesFor(b *Broadcast) map[string]push.Message {
	data := map[string]string{"type": PushTypePromo, "link": b.Link}
	titleKY, bodyKY := b.TitleKY, b.BodyKY
	if titleKY == "" {
		titleKY = b.TitleRU
	}
	if bodyKY == "" {
		bodyKY = b.BodyRU
	}
	return map[string]push.Message{
		i18n.LangRU: {Title: b.TitleRU, Body: b.BodyRU, Data: data},
		i18n.LangKY: {Title: titleKY, Body: bodyKY, Data: data},
	}
}

// ---- Repo: workerStore implementation ----

// NextPending implements workerStore.
func (r *Repo) NextPending(ctx context.Context) (*Broadcast, error) {
	b, err := scanBroadcast(r.db.QueryRowContext(ctx, `
		SELECT `+broadcastColumns+` FROM broadcasts
		WHERE status IN ('queued', 'sending')
		ORDER BY created_at LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

// MarkSending implements workerStore.
func (r *Repo) MarkSending(ctx context.Context, id string) error {
	const q = `
		UPDATE broadcasts SET status = 'sending', started_at = now(),
			targets = (SELECT count(*) FROM device_tokens d JOIN customers c ON c.id = d.customer_id WHERE ` + audienceWhere + `)
		WHERE id = $1 AND status = 'queued'`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}

// NextTargets implements workerStore.
func (r *Repo) NextTargets(ctx context.Context, cursor *string, limit int) ([]Target, error) {
	const q = `
		SELECT d.id, d.fcm_token, c.lang
		FROM device_tokens d JOIN customers c ON c.id = d.customer_id
		WHERE ` + audienceWhere + ` AND ($1::uuid IS NULL OR d.id > $1::uuid)
		ORDER BY d.id
		LIMIT $2`
	rows, err := r.db.QueryContext(ctx, q, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.TokenID, &t.Token, &t.Lang); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RecordBatch implements workerStore.
func (r *Repo) RecordBatch(ctx context.Context, id string, res BatchResult, invalidTokens []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if len(invalidTokens) > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM device_tokens WHERE fcm_token = ANY($1)`, invalidTokens); err != nil {
			return err
		}
	}
	const q = `
		UPDATE broadcasts SET sent = sent + $2, failed = failed + $3, invalid_removed = invalid_removed + $4,
			cursor_token_id = $5
		WHERE id = $1`
	if _, err := tx.ExecContext(ctx, q, id, res.Sent, res.Failed, res.InvalidRemoved, res.Cursor); err != nil {
		return err
	}
	return tx.Commit()
}

// Finish implements workerStore.
func (r *Repo) Finish(ctx context.Context, id, errMsg string) error {
	status, errVal := StatusDone, sql.NullString{}
	if errMsg != "" {
		status, errVal = StatusFailed, sql.NullString{String: errMsg, Valid: true}
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE broadcasts SET status = $2, error = $3, finished_at = now() WHERE id = $1`, id, status, errVal)
	return err
}
