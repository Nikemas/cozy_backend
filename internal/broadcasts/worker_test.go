package broadcasts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/push"
)

// memStore is an in-memory workerStore over a fixed device list.
type memStore struct {
	mu         sync.Mutex
	broadcasts []*Broadcast
	devices    []Target // sorted by TokenID
	deleted    []string
	batches    int
	failNext   int // NextTargets fails this many times
	onBatch    func()
}

func (m *memStore) NextPending(context.Context) (*Broadcast, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.broadcasts {
		if b.Status == StatusQueued || b.Status == StatusSending {
			cp := *b
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *memStore) get(id string) *Broadcast {
	for _, b := range m.broadcasts {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (m *memStore) MarkSending(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.get(id)
	if b.Status == StatusQueued {
		b.Status = StatusSending
		b.Targets = len(m.devices)
	}
	return nil
}

func (m *memStore) NextTargets(_ context.Context, cursor *string, limit int) ([]Target, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext > 0 {
		m.failNext--
		return nil, errors.New("db down")
	}
	var out []Target
	for _, d := range m.devices {
		if cursor != nil && d.TokenID <= *cursor {
			continue
		}
		out = append(out, d)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) RecordBatch(_ context.Context, id string, res BatchResult, invalid []string) error {
	m.mu.Lock()
	b := m.get(id)
	b.Sent += res.Sent
	b.Failed += res.Failed
	b.InvalidRemoved += res.InvalidRemoved
	c := res.Cursor
	b.CursorTokenID = &c
	m.deleted = append(m.deleted, invalid...)
	m.batches++
	hook := m.onBatch
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (m *memStore) Finish(_ context.Context, id, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.get(id)
	if errMsg == "" {
		b.Status = StatusDone
	} else {
		b.Status = StatusFailed
		b.Error = &errMsg
	}
	return nil
}

type recPush struct {
	mu     sync.Mutex
	sent   map[string]push.Message
	errFor map[string]error
}

func (p *recPush) Send(_ context.Context, token string, msg push.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.errFor[token]; err != nil {
		return err
	}
	if p.sent == nil {
		p.sent = map[string]push.Message{}
	}
	p.sent[token] = msg
	return nil
}

func devices(n int, lang func(i int) string) []Target {
	out := make([]Target, n)
	for i := range out {
		out[i] = Target{TokenID: fmt.Sprintf("id-%03d", i), Token: fmt.Sprintf("tok-%03d", i), Lang: lang(i)}
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWorkerSendsInBatchesPerLanguageAndRemovesInvalidTokens(t *testing.T) {
	store := &memStore{
		broadcasts: []*Broadcast{{ID: "b1", Status: StatusQueued, TitleRU: "Скидки", BodyRU: "−20%", TitleKY: "Арзандатуу",
			Link: "/product/p1"}},
		devices: devices(7, func(i int) string {
			if i%2 == 0 {
				return "ky"
			}
			return "ru"
		}),
	}
	p := &recPush{errFor: map[string]error{
		"tok-003": push.ErrInvalidToken,
		"tok-005": errors.New("FCM 503"),
	}}
	w := NewWorker(store, p, WorkerConfig{BatchSize: 3, Concurrency: 2, PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := w.Start(ctx)
	w.Wake()
	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return store.broadcasts[0].Status == StatusDone })
	cancel()
	<-done

	b := store.broadcasts[0]
	if b.Targets != 7 || b.Sent != 5 || b.Failed != 1 || b.InvalidRemoved != 1 {
		t.Errorf("stats = targets %d sent %d failed %d invalid %d, want 7/5/1/1", b.Targets, b.Sent, b.Failed, b.InvalidRemoved)
	}
	if store.batches != 3 {
		t.Errorf("batches = %d, want 3 (7 devices / batch of 3)", store.batches)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "tok-003" {
		t.Errorf("deleted = %v, want [tok-003]", store.deleted)
	}
	ky, ru := p.sent["tok-000"], p.sent["tok-001"]
	if ky.Title != "Арзандатуу" || ky.Body != "−20%" {
		t.Errorf("ky message = %+v, want KY title and RU body fallback", ky)
	}
	if ru.Title != "Скидки" {
		t.Errorf("ru message = %+v", ru)
	}
	if ru.Data["type"] != "promo" || ru.Data["link"] != "/product/p1" {
		t.Errorf("data = %v, want type=promo link=/product/p1", ru.Data)
	}
}

func TestWorkerStopsBetweenBatchesOnShutdownAndResumes(t *testing.T) {
	store := &memStore{
		broadcasts: []*Broadcast{{ID: "b1", Status: StatusQueued, TitleRU: "t", BodyRU: "b"}},
		devices:    devices(6, func(int) string { return "ru" }),
	}
	p := &recPush{}
	ctx, cancel := context.WithCancel(context.Background())
	store.onBatch = func() { cancel() } // shutdown arrives during the first batch
	w := NewWorker(store, p, WorkerConfig{BatchSize: 2, PollInterval: time.Hour})
	<-w.Start(ctx)

	b := store.broadcasts[0]
	if b.Status != StatusSending || b.Sent != 2 || b.CursorTokenID == nil || *b.CursorTokenID != "id-001" {
		t.Fatalf("after shutdown: status %s sent %d cursor %v, want sending/2/id-001", b.Status, b.Sent, b.CursorTokenID)
	}

	// "Restart": a new worker resumes from the cursor — no device twice.
	store.onBatch = nil
	p2 := &recPush{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := NewWorker(store, p2, WorkerConfig{BatchSize: 2, PollInterval: time.Hour}).Start(ctx2)
	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return b.Status == StatusDone })
	cancel2()
	<-done
	if b.Sent != 6 || len(p2.sent) != 4 {
		t.Errorf("resumed: total sent %d, second run sent %d; want 6 and 4", b.Sent, len(p2.sent))
	}
	if _, again := p2.sent["tok-000"]; again {
		t.Error("device from the first batch was sent twice")
	}
}

func TestWorkerGivesUpAfterRepeatedStoreErrors(t *testing.T) {
	store := &memStore{
		broadcasts: []*Broadcast{{ID: "b1", Status: StatusSending, TitleRU: "t", BodyRU: "b"}},
		devices:    devices(1, func(int) string { return "ru" }),
		failNext:   maxAttemptErrors,
	}
	w := NewWorker(store, &recPush{}, WorkerConfig{PollInterval: time.Hour})
	for i := 0; i < maxAttemptErrors; i++ {
		w.drain(context.Background())
	}
	if b := store.broadcasts[0]; b.Status != StatusFailed || b.Error == nil {
		t.Fatalf("status = %s, want failed with an error message", b.Status)
	}
}

func TestNudgeWakesStartedWorker(t *testing.T) {
	store := &memStore{devices: devices(1, func(int) string { return "ru" })}
	w := NewWorker(store, &recPush{}, WorkerConfig{PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := w.Start(ctx)
	store.mu.Lock()
	store.broadcasts = append(store.broadcasts, &Broadcast{ID: "b1", Status: StatusQueued, TitleRU: "t", BodyRU: "b"})
	store.mu.Unlock()
	Nudge()
	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return store.broadcasts[0].Status == StatusDone })
	cancel()
	<-done
}
