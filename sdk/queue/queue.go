// Package queue is the durable work queue a plugin keeps once it has
// accepted custody of an item. Items survive restarts, a re-offer of the same
// delivery id is recognized, and status updates wait here until the stream to
// the host is open.
package queue

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// State of an item in the queue.
const (
	StateQueued    = "queued"
	StateRetrying  = "retrying"
	StateDelivered = "delivered"
	StateFailed    = "failed"
	StateExpired   = "expired"
)

// Row is one queued delivery.
type Row struct {
	DeliveryID  string
	Raw         []byte // the marshaled deliveryv1.Item
	ExpiresAt   time.Time
	Attempts    int
	NextAt      time.Time
	State       string
	LastError   string
	ProviderRef string
}

// Final reports whether the item will not be worked on again.
func (r Row) Final() bool {
	return r.State == StateDelivered || r.State == StateFailed || r.State == StateExpired
}

// Update is a status report waiting to be sent to the host.
type Update struct {
	ID          int64
	DeliveryID  string
	State       int32 // deliveryv1.DeliveryState
	ErrorClass  int32 // deliveryv1.ErrorClass
	Message     string
	ProviderRef string
	At          time.Time
}

type Queue struct {
	db *sql.DB
}

// Open opens or creates the queue database at path.
func Open(path string) (*Queue, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS items (
			delivery_id  TEXT PRIMARY KEY,
			raw          BLOB NOT NULL,
			expires_at   INTEGER NOT NULL,
			attempts     INTEGER NOT NULL DEFAULT 0,
			next_at      INTEGER NOT NULL,
			state        TEXT NOT NULL,
			last_error   TEXT NOT NULL DEFAULT '',
			provider_ref TEXT NOT NULL DEFAULT '',
			updated_at   INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS items_due ON items(state, next_at);
		CREATE TABLE IF NOT EXISTS updates (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			delivery_id  TEXT NOT NULL,
			state        INTEGER NOT NULL,
			error_class  INTEGER NOT NULL,
			message      TEXT NOT NULL,
			provider_ref TEXT NOT NULL,
			at           INTEGER NOT NULL
		);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Queue{db: db}, nil
}

func (q *Queue) Close() error { return q.db.Close() }

// Add stores an item. It returns created=false when the delivery id is already
// known, which is how a re-offer after a lost ack is recognized.
func (q *Queue) Add(deliveryID string, raw []byte, expiresAt, now time.Time) (created bool, existing Row, err error) {
	if raw == nil {
		raw = []byte{} // a nil slice would be stored as NULL
	}
	res, err := q.db.Exec(
		`INSERT OR IGNORE INTO items (delivery_id, raw, expires_at, next_at, state, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		deliveryID, raw, expiresAt.UnixMilli(), now.UnixMilli(), StateQueued, now.UnixMilli())
	if err != nil {
		return false, Row{}, err
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return true, Row{}, nil
	}
	row, ok, err := q.Get(deliveryID)
	if err != nil {
		return false, Row{}, err
	}
	if !ok {
		return false, Row{}, errors.New("queue: item vanished")
	}
	return false, row, nil
}

func scanRow(s interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var exp, next int64
	if err := s.Scan(&r.DeliveryID, &r.Raw, &exp, &r.Attempts, &next, &r.State, &r.LastError, &r.ProviderRef); err != nil {
		return Row{}, err
	}
	r.ExpiresAt = time.UnixMilli(exp)
	r.NextAt = time.UnixMilli(next)
	return r, nil
}

const rowColumns = `delivery_id, raw, expires_at, attempts, next_at, state, last_error, provider_ref`

func (q *Queue) Get(deliveryID string) (Row, bool, error) {
	r, err := scanRow(q.db.QueryRow(`SELECT `+rowColumns+` FROM items WHERE delivery_id = ?`, deliveryID))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, nil
	}
	if err != nil {
		return Row{}, false, err
	}
	return r, true, nil
}

// NextDue returns the oldest unfinished item whose time has come.
func (q *Queue) NextDue(now time.Time) (Row, bool, error) {
	r, err := scanRow(q.db.QueryRow(
		`SELECT `+rowColumns+` FROM items WHERE state IN (?, ?) AND next_at <= ? ORDER BY next_at, delivery_id LIMIT 1`,
		StateQueued, StateRetrying, now.UnixMilli()))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, nil
	}
	if err != nil {
		return Row{}, false, err
	}
	return r, true, nil
}

// NextWake returns when the earliest unfinished item becomes due.
func (q *Queue) NextWake() (time.Time, bool, error) {
	var ms sql.NullInt64
	if err := q.db.QueryRow(`SELECT MIN(next_at) FROM items WHERE state IN (?, ?)`, StateQueued, StateRetrying).Scan(&ms); err != nil {
		return time.Time{}, false, err
	}
	if !ms.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(ms.Int64), true, nil
}

func (q *Queue) set(id, state, lastErr, providerRef string, next time.Time, bumpAttempt bool, now time.Time) error {
	attempt := 0
	if bumpAttempt {
		attempt = 1
	}
	_, err := q.db.Exec(
		`UPDATE items SET state = ?, last_error = ?, provider_ref = CASE WHEN ? != '' THEN ? ELSE provider_ref END,
		 next_at = ?, attempts = attempts + ?, updated_at = ? WHERE delivery_id = ?`,
		state, lastErr, providerRef, providerRef, next.UnixMilli(), attempt, now.UnixMilli(), id)
	return err
}

func (q *Queue) MarkDelivered(id, providerRef string, now time.Time) error {
	return q.set(id, StateDelivered, "", providerRef, now, true, now)
}

func (q *Queue) MarkFailed(id, msg string, now time.Time) error {
	return q.set(id, StateFailed, msg, "", now, true, now)
}

func (q *Queue) MarkExpired(id string, now time.Time) error {
	return q.set(id, StateExpired, "", "", now, false, now)
}

// MarkRetry records a failed attempt and schedules the next one.
func (q *Queue) MarkRetry(id, msg string, next, now time.Time) error {
	return q.set(id, StateRetrying, msg, "", next, true, now)
}

// AddUpdate queues a status report for the host.
func (q *Queue) AddUpdate(u Update) error {
	_, err := q.db.Exec(
		`INSERT INTO updates (delivery_id, state, error_class, message, provider_ref, at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.DeliveryID, u.State, u.ErrorClass, u.Message, u.ProviderRef, u.At.UnixMilli())
	return err
}

func (q *Queue) PendingUpdates(limit int) ([]Update, error) {
	rows, err := q.db.Query(
		`SELECT id, delivery_id, state, error_class, message, provider_ref, at FROM updates ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var u Update
		var at int64
		if err := rows.Scan(&u.ID, &u.DeliveryID, &u.State, &u.ErrorClass, &u.Message, &u.ProviderRef, &at); err != nil {
			return nil, err
		}
		u.At = time.UnixMilli(at)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (q *Queue) DeleteUpdate(id int64) error {
	_, err := q.db.Exec(`DELETE FROM updates WHERE id = ?`, id)
	return err
}

// Purge forgets finished items last touched before cutoff. Keeping them until
// then is what lets a re-offer be recognized.
func (q *Queue) Purge(cutoff time.Time) (int64, error) {
	res, err := q.db.Exec(
		`DELETE FROM items WHERE state IN (?, ?, ?) AND updated_at < ?`,
		StateDelivered, StateFailed, StateExpired, cutoff.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("queue purge: %w", err)
	}
	return res.RowsAffected()
}
