// Package postgres persists execution events and trades to PostgreSQL.
//
// The writer owns its DTOs and a narrow DB interface so it can be tested
// without a live database and reused by the outbound pipeline without
// depending on engine types.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	// ErrNilWriter is returned when a method is called on a nil *Writer.
	ErrNilWriter = errors.New("postgres: nil writer")
	// ErrNilDB is returned when a Writer has no DB implementation.
	ErrNilDB = errors.New("postgres: nil db")
	// ErrWriterClosed is returned after Close has been called.
	ErrWriterClosed = errors.New("postgres: writer closed")
	// ErrEmptyDSN is returned by Open when the DSN is empty.
	ErrEmptyDSN = errors.New("postgres: empty dsn")
)

// DB is the subset of *sql.DB used by Writer. Keeping it narrow allows tests
// and callers to substitute their own implementation.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PingContext(ctx context.Context) error
	Close() error
}

// Rows is the subset of *sql.Rows that LoadPositions drains. The narrow
// interface lets tests cover scanning without a database.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// Writer persists orders, execution events, trades and position snapshots. Its
// methods are safe for concurrent use. Writer does not retry failed statements:
// the outbound worker owns retry policy, and every method honors the caller's
// context.
type Writer struct {
	db DB

	closed atomic.Bool
	once   sync.Once
	err    error
}

// NewWriter wraps db with the persistence API. Close delegates to db.Close.
func NewWriter(db DB) *Writer {
	return &Writer{db: db}
}

// Open connects to PostgreSQL through the pgx stdlib driver and verifies the
// connection with a single ping. It does not retry.
func Open(ctx context.Context, dsn string) (*Writer, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, ErrEmptyDSN
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return NewWriter(db), nil
}

// Close closes the underlying DB. It is idempotent and returns the first close
// error to every caller.
func (w *Writer) Close() error {
	if w == nil {
		return ErrNilWriter
	}

	w.once.Do(func() {
		w.closed.Store(true)
		if w.db == nil {
			w.err = ErrNilDB
			return
		}
		if err := w.db.Close(); err != nil {
			w.err = fmt.Errorf("postgres: close: %w", err)
		}
	})
	return w.err
}

// Ping checks that the database is reachable.
func (w *Writer) Ping(ctx context.Context) error {
	if err := w.ready(); err != nil {
		return err
	}
	if err := w.db.PingContext(ctx); err != nil {
		return fmt.Errorf("postgres: ping: %w", err)
	}
	return nil
}

// UpsertOrder inserts an order or updates the mutable columns when the ID
// already exists. created_at is preserved on conflict.
func (w *Writer) UpsertOrder(ctx context.Context, o Order) error {
	if err := w.ready(); err != nil {
		return err
	}

	_, err := w.db.ExecContext(ctx, upsertOrderSQL,
		o.ID,
		o.ClientOrderID,
		o.AccountID,
		o.Symbol,
		o.Side,
		o.OrderType,
		o.TimeInForce,
		o.Price,
		o.Quantity,
		o.Leaves,
		o.Filled,
		o.Status,
		o.CreatedAt,
		o.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert order %d: %w", o.ID, err)
	}
	return nil
}

// InsertTrade inserts a trade, ignoring duplicates with the same ID.
func (w *Writer) InsertTrade(ctx context.Context, t Trade) error {
	if err := w.ready(); err != nil {
		return err
	}

	_, err := w.db.ExecContext(ctx, insertTradeSQL,
		t.ID,
		t.Symbol,
		t.Price,
		t.Quantity,
		t.BuyOrderID,
		t.SellOrderID,
		nullableID(t.MakerOrderID),
		t.TradedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: insert trade %d: %w", t.ID, err)
	}
	return nil
}

// InsertExecEvent inserts an execution event, ignoring duplicates with the
// same exec ID.
func (w *Writer) InsertExecEvent(ctx context.Context, e ExecEvent) error {
	if err := w.ready(); err != nil {
		return err
	}

	_, err := w.db.ExecContext(ctx, insertExecEventSQL,
		e.ExecID,
		e.Seq,
		e.OrderID,
		e.ClientOrderID,
		e.AccountID,
		e.Symbol,
		e.Side,
		e.ExecType,
		e.Status,
		e.LastPx,
		e.LastQty,
		e.CumQty,
		e.LeavesQty,
		e.Liquidity,
		nullableID(e.TradeID),
		e.Reason,
		e.Venue,
		e.ReportedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: insert exec event %d: %w", e.ExecID, err)
	}
	return nil
}

// UpsertPosition inserts one position or overwrites the mutable columns when
// (account_id, symbol) already exists.
func (w *Writer) UpsertPosition(ctx context.Context, p Position) error {
	if err := w.ready(); err != nil {
		return err
	}
	if err := w.upsertPosition(ctx, p); err != nil {
		return fmt.Errorf("postgres: upsert position account=%d symbol=%q: %w", p.AccountID, p.Symbol, err)
	}
	return nil
}

// UpsertPositions writes a batch of positions. The DB interface has no
// transaction support, so each entry is written with its own statement: a
// failure leaves previously written entries committed and the returned error
// names the failing index. Callers that need all-or-nothing semantics must
// serialize the batch themselves.
func (w *Writer) UpsertPositions(ctx context.Context, positions []Position) error {
	if err := w.ready(); err != nil {
		return err
	}
	for i, p := range positions {
		if err := w.upsertPosition(ctx, p); err != nil {
			return fmt.Errorf("postgres: upsert positions[%d] account=%d symbol=%q: %w",
				i, p.AccountID, p.Symbol, err)
		}
	}
	return nil
}

// LoadPositions reads the whole position table ordered by account and symbol.
// It returns a nil slice when the table is empty.
func (w *Writer) LoadPositions(ctx context.Context) ([]Position, error) {
	if err := w.ready(); err != nil {
		return nil, err
	}

	rows, err := w.db.QueryContext(ctx, loadPositionsSQL)
	if err != nil {
		return nil, fmt.Errorf("postgres: load positions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanPositions(rows)
}

func (w *Writer) upsertPosition(ctx context.Context, p Position) error {
	_, err := w.db.ExecContext(ctx, upsertPositionSQL,
		p.AccountID,
		p.Symbol,
		p.Qty,
		p.AvgCost,
		p.Mark,
		p.Realized,
		p.UpdatedAt,
	)
	return err
}

// scanPositions drains rows using the column order of loadPositionsSQL.
func scanPositions(rows Rows) ([]Position, error) {
	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(
			&p.AccountID,
			&p.Symbol,
			&p.Qty,
			&p.AvgCost,
			&p.Mark,
			&p.Realized,
			&p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: load positions: scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: load positions: %w", err)
	}
	return out, nil
}

func (w *Writer) ready() error {
	if w == nil {
		return ErrNilWriter
	}
	if w.db == nil {
		return ErrNilDB
	}
	if w.closed.Load() {
		return ErrWriterClosed
	}
	return nil
}

// nullableID maps the zero value to NULL. Snowflake IDs are always positive, so
// zero means "not set" for optional references such as trade_id and
// maker_order_id.
func nullableID(id uint64) any {
	if id == 0 {
		return nil
	}
	return id
}

const upsertOrderSQL = `INSERT INTO orders (
	id,
	client_order_id,
	account_id,
	symbol,
	side,
	order_type,
	time_in_force,
	price,
	quantity,
	leaves,
	filled,
	status,
	created_at,
	updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (id) DO UPDATE SET
	client_order_id = EXCLUDED.client_order_id,
	account_id = EXCLUDED.account_id,
	symbol = EXCLUDED.symbol,
	side = EXCLUDED.side,
	order_type = EXCLUDED.order_type,
	time_in_force = EXCLUDED.time_in_force,
	price = EXCLUDED.price,
	quantity = EXCLUDED.quantity,
	leaves = EXCLUDED.leaves,
	filled = EXCLUDED.filled,
	status = EXCLUDED.status,
	updated_at = EXCLUDED.updated_at`

const insertTradeSQL = `INSERT INTO trades (
	id,
	symbol,
	price,
	quantity,
	buy_order_id,
	sell_order_id,
	maker_order_id,
	traded_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO NOTHING`

const insertExecEventSQL = `INSERT INTO exec_events (
	exec_id,
	seq,
	order_id,
	client_order_id,
	account_id,
	symbol,
	side,
	exec_type,
	status,
	last_px,
	last_qty,
	cum_qty,
	leaves_qty,
	liquidity,
	trade_id,
	reason,
	venue,
	reported_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
ON CONFLICT (exec_id) DO NOTHING`

const upsertPositionSQL = `INSERT INTO positions (
	account_id,
	symbol,
	qty,
	avg_cost,
	mark,
	realized,
	updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (account_id, symbol) DO UPDATE SET
	qty = EXCLUDED.qty,
	avg_cost = EXCLUDED.avg_cost,
	mark = EXCLUDED.mark,
	realized = EXCLUDED.realized,
	updated_at = EXCLUDED.updated_at`

const loadPositionsSQL = `SELECT
	account_id,
	symbol,
	qty,
	avg_cost,
	mark,
	realized,
	updated_at
FROM positions
ORDER BY account_id, symbol`
