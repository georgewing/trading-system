package postgres

// Risk event persistence: the audit trail for risk decisions and state
// changes. Reads are paginated by descending id so the newest events come
// first and clients can page with before_id.

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultRiskEventLimit = 50
	maxRiskEventLimit     = 500
)

// RiskEvent is the persisted form of one risk decision or state change. It is
// defined here instead of reusing risk.Event so persistence does not break
// when the risk package evolves; callers convert the event's unix milliseconds
// into TS.
type RiskEvent struct {
	ID        int64
	TS        time.Time
	AccountID uint64
	Symbol    string
	Strategy  string
	OrderID   uint64
	Code      string
	Action    string
	Message   string
}

// RiskEventQuery selects a page of risk events. Zero values mean "no filter":
// AccountID 0 and BeforeID 0 match every row, empty Symbol and Code match every
// row. Limit is clamped to [1, maxRiskEventLimit] with 0 falling back to
// defaultRiskEventLimit.
type RiskEventQuery struct {
	Limit     int
	AccountID uint64
	Symbol    string
	Code      string
	BeforeID  int64
}

// InsertRiskEvent appends one risk event. The id is assigned by the database.
func (w *Writer) InsertRiskEvent(ctx context.Context, e RiskEvent) error {
	if err := w.ready(); err != nil {
		return err
	}

	_, err := w.db.ExecContext(ctx, insertRiskEventSQL,
		e.TS,
		e.AccountID,
		e.Symbol,
		e.Strategy,
		e.OrderID,
		e.Code,
		e.Action,
		e.Message,
	)
	if err != nil {
		return fmt.Errorf("postgres: insert risk event: %w", err)
	}
	return nil
}

// ListRiskEvents reads one page of risk events ordered by descending id. It
// returns a nil slice when no row matches.
func (w *Writer) ListRiskEvents(ctx context.Context, q RiskEventQuery) ([]RiskEvent, error) {
	if err := w.ready(); err != nil {
		return nil, err
	}

	rows, err := w.db.QueryContext(ctx, listRiskEventsSQL,
		q.AccountID,
		q.Symbol,
		q.Code,
		q.BeforeID,
		clampRiskEventLimit(q.Limit),
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list risk events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanRiskEvents(rows)
}

// clampRiskEventLimit keeps the page size within the supported range.
func clampRiskEventLimit(limit int) int {
	if limit <= 0 {
		return defaultRiskEventLimit
	}
	if limit > maxRiskEventLimit {
		return maxRiskEventLimit
	}
	return limit
}

// scanRiskEvents drains rows using the column order of listRiskEventsSQL.
func scanRiskEvents(rows Rows) ([]RiskEvent, error) {
	var out []RiskEvent
	for rows.Next() {
		var e RiskEvent
		if err := rows.Scan(
			&e.ID,
			&e.TS,
			&e.AccountID,
			&e.Symbol,
			&e.Strategy,
			&e.OrderID,
			&e.Code,
			&e.Action,
			&e.Message,
		); err != nil {
			return nil, fmt.Errorf("postgres: list risk events: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list risk events: %w", err)
	}
	return out, nil
}

const insertRiskEventSQL = `INSERT INTO risk_events (
	ts,
	account_id,
	symbol,
	strategy,
	order_id,
	code,
	action,
	message
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

const listRiskEventsSQL = `SELECT
	id,
	ts,
	account_id,
	symbol,
	strategy,
	order_id,
	code,
	action,
	message
FROM risk_events
WHERE ($1 = 0 OR account_id = $1)
	AND ($2 = '' OR symbol = $2)
	AND ($3 = '' OR code = $3)
	AND ($4 = 0 OR id < $4)
ORDER BY id DESC
LIMIT $5`
