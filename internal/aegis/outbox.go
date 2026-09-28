package aegis

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const (
	// maxPending caps the events waiting to be sent; the oldest go first.
	maxPending = 10_000
	// sentRetention keeps sent keys long enough for rescans to skip them.
	sentRetention = 7 * 24 * time.Hour
)

// outbox is the durable queue of events to send, in the agent's shared
// SQLite database. Sent events stay as a ledger for a week, so rescanning the
// operation log never sends an event twice.
type outbox struct {
	db *sql.DB
}

type pendingEvent struct {
	Key   string
	OpID  int64
	Phase string
}

func newOutbox(db *sql.DB) (*outbox, error) {
	o := &outbox{db: db}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS aegis_outbox (
			key TEXT PRIMARY KEY,
			op_id INTEGER NOT NULL,
			phase TEXT NOT NULL,
			created_ms INTEGER NOT NULL,
			sent_ms INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS aegis_outbox_pending ON aegis_outbox (sent_ms, created_ms)`,
		`CREATE TABLE IF NOT EXISTS aegis_state (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return nil, fmt.Errorf("create aegis outbox: %w", err)
		}
	}
	return o, nil
}

func (o *outbox) state(key string) (string, error) {
	var value string
	err := o.db.QueryRow(`SELECT v FROM aegis_state WHERE k = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (o *outbox) setState(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO aegis_state (k, v) VALUES (?, ?)`, key, value)
	return err
}

// installID is this installation's random identity, created once. Operation
// ids start over when the data directory is lost; event keys include this id
// so a reinstall never collides with what was sent before.
func (o *outbox) installID() (string, error) {
	existing, err := o.state("install_id")
	if err != nil || existing != "" {
		return existing, err
	}
	id := uuid.New().String()
	tx, err := o.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO aegis_state (k, v) VALUES ('install_id', ?)`, id); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return o.state("install_id")
}

func (o *outbox) watermark() (int64, error) {
	value, err := o.state("watermark")
	if err != nil || value == "" {
		return 0, err
	}
	return strconv.ParseInt(value, 10, 64)
}

// record adds new events (keys already pending or sent are skipped) and
// moves the scan watermark, atomically.
func (o *outbox) record(ctx context.Context, events []pendingEvent, watermark int64, now time.Time) (int, error) {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	added := 0
	for _, event := range events {
		result, err := tx.Exec(
			`INSERT OR IGNORE INTO aegis_outbox (key, op_id, phase, created_ms) VALUES (?, ?, ?, ?)`,
			event.Key, event.OpID, event.Phase, now.UnixMilli(),
		)
		if err != nil {
			return 0, err
		}
		if rows, _ := result.RowsAffected(); rows > 0 {
			added++
		}
	}
	if err := o.setState(tx, "watermark", strconv.FormatInt(watermark, 10)); err != nil {
		return 0, err
	}
	return added, tx.Commit()
}

// pending lists the oldest unsent events.
func (o *outbox) pending(limit int) ([]pendingEvent, error) {
	rows, err := o.db.Query(
		`SELECT key, op_id, phase FROM aegis_outbox WHERE sent_ms IS NULL ORDER BY created_ms, op_id, key LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []pendingEvent
	for rows.Next() {
		var event pendingEvent
		if err := rows.Scan(&event.Key, &event.OpID, &event.Phase); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (o *outbox) pendingCount() (int, error) {
	var count int
	err := o.db.QueryRow(`SELECT COUNT(*) FROM aegis_outbox WHERE sent_ms IS NULL`).Scan(&count)
	return count, err
}

// markSent moves events to the ledger: Aegis Cloud has them, or refused them for good.
func (o *outbox) markSent(keys []string, now time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := o.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, key := range keys {
		if _, err := tx.Exec(`UPDATE aegis_outbox SET sent_ms = ? WHERE key = ?`, now.UnixMilli(), key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// trim forgets the ledger older than a week and caps the pending queue.
func (o *outbox) trim(now time.Time) (dropped int64, err error) {
	if _, err := o.db.Exec(
		`DELETE FROM aegis_outbox WHERE sent_ms IS NOT NULL AND sent_ms < ?`,
		now.Add(-sentRetention).UnixMilli(),
	); err != nil {
		return 0, err
	}
	result, err := o.db.Exec(
		`DELETE FROM aegis_outbox WHERE key IN (
			SELECT key FROM aegis_outbox WHERE sent_ms IS NULL
			ORDER BY created_ms DESC, op_id DESC LIMIT -1 OFFSET ?
		)`,
		maxPending,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
