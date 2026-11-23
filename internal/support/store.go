package support

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Nikemas/cozy_backend/internal/dbtx"
)

// Store is the persistence Service depends on (SQLStore in production, a
// fake in unit tests). Methods return the package's apperr values
// (errTicketNotFound, errTicketClosed, errTooManyTickets) for domain
// outcomes, anything else for infrastructure failures.
type Store interface {
	// CustomerContact is the account's name ("" if none) and phone;
	// found=false for an unknown or deleted customer.
	CustomerContact(ctx context.Context, customerID string) (name, phone string, found bool, err error)
	// OrderOfCustomer reports whether orderID belongs to customerID.
	OrderOfCustomer(ctx context.Context, orderID, customerID string) (bool, error)
	// Create inserts the request with its first message, refusing with
	// errTooManyTickets when the customer (or, for a guest, the phone)
	// already created maxPerHour requests within the last hour.
	Create(ctx context.Context, in NewTicket, maxPerHour int) (string, error)
	// Get returns the request with its conversation, or errTicketNotFound.
	Get(ctx context.Context, id string) (*Ticket, error)
	ListByCustomer(ctx context.Context, customerID string, limit, offset int) ([]Summary, int, error)
	List(ctx context.Context, f ListFilter) ([]Summary, int, error)
	// AddMessage appends a message unless the request is closed
	// (errTicketClosed). A staff reply moves a new request to in_progress.
	AddMessage(ctx context.Context, ticketID string, author Author, staffID *string, body string) (Message, error)
	// CustomerMessagesSince counts customerID's follow-ups since t.
	CustomerMessagesSince(ctx context.Context, customerID string, since time.Time) (int, error)
	// SetStatus changes the status and returns the previous one.
	SetStatus(ctx context.Context, id string, status Status) (Status, error)
	CountNew(ctx context.Context) (int, error)
}

// NewTicket is a validated request ready to insert.
type NewTicket struct {
	CustomerID *string
	Name       string
	Phone      string
	Topic      Topic
	OrderID    *string
	Channel    Channel
	Message    string
}

// SQLStore is the Postgres Store (migration 000052).
type SQLStore struct {
	db *sql.DB
}

// NewSQLStore returns a Store over db.
func NewSQLStore(db *sql.DB) *SQLStore { return &SQLStore{db: db} }

var _ Store = (*SQLStore)(nil)

func (s *SQLStore) CustomerContact(ctx context.Context, customerID string) (string, string, bool, error) {
	var name, phone string
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(name, ''), phone FROM customers WHERE id = $1 AND deleted_at IS NULL`, customerID).
		Scan(&name, &phone)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("support: load customer contact: %w", err)
	}
	return name, phone, true, nil
}

func (s *SQLStore) OrderOfCustomer(ctx context.Context, orderID, customerID string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1 AND customer_id = $2)`, orderID, customerID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("support: check order owner: %w", err)
	}
	return ok, nil
}

func (s *SQLStore) Create(ctx context.Context, in NewTicket, maxPerHour int) (string, error) {
	var id string
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		// Serialize concurrent creations by the same customer/phone so the
		// hourly count below can't be raced past the limit.
		lockKey := "support:" + in.Phone
		if in.CustomerID != nil {
			lockKey = "support:" + *in.CustomerID
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
			return fmt.Errorf("support: lock: %w", err)
		}
		if maxPerHour > 0 {
			var n int
			err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM support_tickets
				WHERE created_at > now() - interval '1 hour'
				  AND (phone = $1 OR ($2::uuid IS NOT NULL AND customer_id = $2::uuid))`,
				in.Phone, in.CustomerID).Scan(&n)
			if err != nil {
				return fmt.Errorf("support: count recent tickets: %w", err)
			}
			if n >= maxPerHour {
				return errTooManyTickets
			}
		}
		err := tx.QueryRowContext(ctx, `
			INSERT INTO support_tickets (customer_id, name, phone, topic, order_id, channel)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			in.CustomerID, in.Name, in.Phone, string(in.Topic), in.OrderID, string(in.Channel)).Scan(&id)
		if err != nil {
			return fmt.Errorf("support: insert ticket: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO support_ticket_messages (ticket_id, author, body) VALUES ($1, 'customer', $2)`,
			id, in.Message); err != nil {
			return fmt.Errorf("support: insert first message: %w", err)
		}
		return nil
	})
	return id, err
}

const ticketColumns = `
	t.id, t.number, t.customer_id, t.name, t.phone, t.topic, t.order_id,
	COALESCE(o.order_number, ''), t.channel, t.status, t.created_at, t.updated_at, t.closed_at`

func (s *SQLStore) Get(ctx context.Context, id string) (*Ticket, error) {
	var t Ticket
	err := s.db.QueryRowContext(ctx, `SELECT `+ticketColumns+`
		FROM support_tickets t LEFT JOIN orders o ON o.id = t.order_id
		WHERE t.id = $1`, id).
		Scan(&t.ID, &t.Number, &t.CustomerID, &t.Name, &t.Phone, &t.Topic, &t.OrderID,
			&t.OrderNumber, &t.Channel, &t.Status, &t.CreatedAt, &t.UpdatedAt, &t.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errTicketNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("support: load ticket: %w", err)
	}
	msgs, err := s.messages(ctx, id)
	if err != nil {
		return nil, err
	}
	t.Messages = msgs
	return &t, nil
}

func (s *SQLStore) messages(ctx context.Context, ticketID string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.author, m.staff_id, COALESCE(st.name, ''), m.body, m.created_at
		FROM support_ticket_messages m LEFT JOIN staff st ON st.id = m.staff_id
		WHERE m.ticket_id = $1
		ORDER BY m.created_at, m.id`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("support: load messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Author, &m.StaffID, &m.StaffName, &m.Body, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("support: scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// summarySelect is a list row: the request, its message count and its
// latest message.
const summarySelect = `SELECT ` + ticketColumns + `,
	mc.cnt, lm.id, lm.author, lm.body, lm.created_at
	FROM support_tickets t
	LEFT JOIN orders o ON o.id = t.order_id
	CROSS JOIN LATERAL (SELECT COUNT(*) AS cnt FROM support_ticket_messages WHERE ticket_id = t.id) mc
	LEFT JOIN LATERAL (
		SELECT id, author, body, created_at FROM support_ticket_messages
		WHERE ticket_id = t.id ORDER BY created_at DESC, id DESC LIMIT 1
	) lm ON true`

func scanSummaries(rows *sql.Rows) ([]Summary, error) {
	defer func() { _ = rows.Close() }()
	out := []Summary{}
	for rows.Next() {
		var sm Summary
		var lmID, lmAuthor, lmBody sql.NullString
		var lmAt sql.NullTime
		if err := rows.Scan(&sm.ID, &sm.Number, &sm.CustomerID, &sm.Name, &sm.Phone, &sm.Topic, &sm.OrderID,
			&sm.OrderNumber, &sm.Channel, &sm.Status, &sm.CreatedAt, &sm.UpdatedAt, &sm.ClosedAt,
			&sm.MessagesCount, &lmID, &lmAuthor, &lmBody, &lmAt); err != nil {
			return nil, fmt.Errorf("support: scan ticket row: %w", err)
		}
		sm.LastMessage = Message{ID: lmID.String, Author: Author(lmAuthor.String), Body: lmBody.String, CreatedAt: lmAt.Time}
		out = append(out, sm)
	}
	return out, rows.Err()
}

func (s *SQLStore) ListByCustomer(ctx context.Context, customerID string, limit, offset int) ([]Summary, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM support_tickets WHERE customer_id = $1`, customerID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("support: count customer tickets: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, summarySelect+`
		WHERE t.customer_id = $1
		ORDER BY t.updated_at DESC, t.number DESC
		LIMIT $2 OFFSET $3`, customerID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("support: list customer tickets: %w", err)
	}
	list, err := scanSummaries(rows)
	return list, total, err
}

// adminWhere filters by status, topic and a search query matching the
// name, the phone digits or the request number.
const adminWhere = `
	WHERE ($1 = '' OR t.status = $1)
	  AND ($2 = '' OR t.topic = $2)
	  AND ($3 = '' OR t.name ILIKE '%' || $3 || '%' ESCAPE '\'
	       OR ($4 <> '' AND t.phone LIKE '%' || $4 || '%')
	       OR ($5::bigint IS NOT NULL AND t.number = $5::bigint))`

// minPhoneSearchDigits keeps a search like "1" from matching every phone.
const minPhoneSearchDigits = 3

func adminArgs(f ListFilter) []any {
	q := strings.TrimSpace(f.Query)
	var digits strings.Builder
	for _, c := range q {
		if c >= '0' && c <= '9' {
			digits.WriteRune(c)
		}
	}
	d := digits.String()
	var number any
	if n, err := strconv.ParseInt(strings.TrimPrefix(q, "#"), 10, 64); err == nil && n > 0 {
		number = n
	}
	// Local format "0555 …" → stored "+996555…": drop the trunk 0.
	if strings.HasPrefix(d, "0") && len(d) > minPhoneSearchDigits {
		d = d[1:]
	}
	if len(d) < minPhoneSearchDigits {
		d = ""
	}
	return []any{string(f.Status), string(f.Topic), escapeLike(q), d, number}
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *SQLStore) List(ctx context.Context, f ListFilter) ([]Summary, int, error) {
	args := adminArgs(f)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM support_tickets t`+adminWhere, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("support: count tickets: %w", err)
	}
	offset := (f.Page - 1) * f.PageSize
	rows, err := s.db.QueryContext(ctx, summarySelect+adminWhere+`
		ORDER BY t.created_at DESC, t.number DESC
		LIMIT $6 OFFSET $7`, append(args, f.PageSize, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("support: list tickets: %w", err)
	}
	list, err := scanSummaries(rows)
	return list, total, err
}

func (s *SQLStore) AddMessage(ctx context.Context, ticketID string, author Author, staffID *string, body string) (Message, error) {
	m := Message{Author: author, StaffID: staffID, Body: body}
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var status Status
		err := tx.QueryRowContext(ctx, `SELECT status FROM support_tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return errTicketNotFound
		}
		if err != nil {
			return fmt.Errorf("support: lock ticket: %w", err)
		}
		if status == StatusClosed {
			return errTicketClosed
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO support_ticket_messages (ticket_id, author, staff_id, body)
			VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
			ticketID, string(author), staffID, body).Scan(&m.ID, &m.CreatedAt); err != nil {
			return fmt.Errorf("support: insert message: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE support_tickets
			SET updated_at = now(),
			    status = CASE WHEN $2 = 'staff' AND status = 'new' THEN 'in_progress' ELSE status END
			WHERE id = $1`, ticketID, string(author))
		if err != nil {
			return fmt.Errorf("support: touch ticket: %w", err)
		}
		return nil
	})
	return m, err
}

func (s *SQLStore) CustomerMessagesSince(ctx context.Context, customerID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM support_ticket_messages m
		JOIN support_tickets t ON t.id = m.ticket_id
		WHERE t.customer_id = $1 AND m.author = 'customer' AND m.created_at > $2`, customerID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("support: count customer messages: %w", err)
	}
	return n, nil
}

func (s *SQLStore) SetStatus(ctx context.Context, id string, status Status) (Status, error) {
	var from Status
	err := dbtx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT status FROM support_tickets WHERE id = $1 FOR UPDATE`, id).Scan(&from)
		if errors.Is(err, sql.ErrNoRows) {
			return errTicketNotFound
		}
		if err != nil {
			return fmt.Errorf("support: lock ticket: %w", err)
		}
		if from == status {
			return nil
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE support_tickets
			SET status = $2, updated_at = now(),
			    closed_at = CASE WHEN $2 = 'closed' THEN now() ELSE NULL END
			WHERE id = $1`, id, string(status))
		if err != nil {
			return fmt.Errorf("support: update status: %w", err)
		}
		return nil
	})
	return from, err
}

func (s *SQLStore) CountNew(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM support_tickets WHERE status = 'new'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("support: count new tickets: %w", err)
	}
	return n, nil
}
