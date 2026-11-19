package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"trading-system/internal/hft/engine"
	"trading-system/internal/hft/risk"
	"trading-system/internal/pipeline"
	"trading-system/internal/storage/clickhouse"
	"trading-system/internal/storage/postgres"
	tradinghttp "trading-system/internal/transport/http"
)

// newPersistenceSinks builds the optional durable sinks. The returned
// *postgres.Writer (if any) must be closed after the outbound worker stops.
func newPersistenceSinks(ctx context.Context, cfg *Config) ([]pipeline.ExecReportSink, *postgres.Writer, error) {
	var sinks []pipeline.ExecReportSink
	var pgWriter *postgres.Writer

	if cfg.Storage.PostgresEnabled {
		w, err := postgres.Open(ctx, cfg.Storage.PostgresDSN)
		if err != nil {
			return nil, nil, err
		}
		pgWriter = w
		sinks = append(sinks, newPGExecSink(w))
	}

	if cfg.Storage.ClickHouseEnabled {
		timeout := clickhouseTimeout(cfg)
		chWriter, err := clickhouse.NewWriter(clickhouse.Config{
			URL:      cfg.Storage.ClickHouseURL,
			Database: cfg.Storage.ClickHouseDatabase,
			Table:    cfg.Storage.ClickHouseTable,
			Username: os.Getenv("CLICKHOUSE_USER"),
			Password: os.Getenv("CLICKHOUSE_PASSWORD"),
			Timeout:  timeout,
		})
		if err != nil {
			closePGWriter(pgWriter)
			return nil, nil, err
		}

		pingCtx, cancel := context.WithTimeout(ctx, timeout)
		err = chWriter.Ping(pingCtx)
		cancel()
		if err != nil {
			closePGWriter(pgWriter)
			return nil, nil, fmt.Errorf("clickhouse: %w", err)
		}
		sinks = append(sinks, newCHTradeSink(chWriter))
	}

	return sinks, pgWriter, nil
}

func closePGWriter(w *postgres.Writer) {
	if w != nil {
		_ = w.Close()
	}
}

// clickhouseTimeout normalizes storage.clickhouse_timeout_seconds.
func clickhouseTimeout(cfg *Config) time.Duration {
	timeout := time.Duration(cfg.Storage.ClickHouseTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return timeout
}

// newCHKlineReader builds the read-only history reader. It shares the writer
// connection settings but reads the kline view, so clickhouse_table (the tick
// table) is intentionally ignored.
func newCHKlineReader(cfg *Config) (*clickhouse.Reader, error) {
	return clickhouse.NewReader(clickhouse.ReaderConfig{
		URL:      cfg.Storage.ClickHouseURL,
		Database: cfg.Storage.ClickHouseDatabase,
		Username: os.Getenv("CLICKHOUSE_USER"),
		Password: os.Getenv("CLICKHOUSE_PASSWORD"),
		Timeout:  clickhouseTimeout(cfg),
	})
}

func restorePositions(ctx context.Context, w *postgres.Writer, positions *risk.Positions) error {
	rows, err := w.LoadPositions(ctx)
	if err != nil {
		return err
	}

	entries := make(map[risk.PositionKey]risk.Position, len(rows))
	for _, row := range rows {
		entries[risk.PositionKey{AccountID: row.AccountID, Symbol: row.Symbol}] = risk.Position{
			Qty:      row.Qty,
			AvgCost:  row.AvgCost,
			Mark:     row.Mark,
			Realized: row.Realized,
		}
	}
	positions.Restore(entries)
	return nil
}

func persistPositions(ctx context.Context, w *postgres.Writer, positions *risk.Positions) error {
	snap := positions.SnapshotAll()
	if len(snap) == 0 {
		return nil
	}

	batch := make([]postgres.Position, 0, len(snap))
	now := time.Now().UTC()
	for key, pos := range snap {
		batch = append(batch, postgres.Position{
			AccountID: key.AccountID,
			Symbol:    key.Symbol,
			Qty:       pos.Qty,
			AvgCost:   pos.AvgCost,
			Mark:      pos.Mark,
			Realized:  pos.Realized,
			UpdatedAt: now,
		})
	}
	return w.UpsertPositions(ctx, batch)
}

// positionSnapshotLoop persists a periodic snapshot so a crash loses at most one
// interval of position changes. A final snapshot is still written on shutdown.
func positionSnapshotLoop(ctx context.Context, w *postgres.Writer, positions *risk.Positions, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			saveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := persistPositions(saveCtx, w, positions); err != nil {
				log.Printf("periodic position snapshot failed: %v", err)
			}
			cancel()
		}
	}
}

type pgExecSink struct {
	writer *postgres.Writer
}

func newPGExecSink(w *postgres.Writer) *pgExecSink { return &pgExecSink{writer: w} }

func (s *pgExecSink) Name() string { return "postgres" }

func (s *pgExecSink) HandleExecReport(ctx context.Context, report engine.ExecReport) error {
	if err := s.writer.UpsertOrder(ctx, toPGOrder(report)); err != nil {
		return err
	}
	if err := s.writer.InsertExecEvent(ctx, toPGExecEvent(report)); err != nil {
		return err
	}
	if report.Trade == nil {
		return nil
	}
	return s.writer.InsertTrade(ctx, toPGTrade(report))
}

type chTradeSink struct {
	writer *clickhouse.Writer
}

func newCHTradeSink(w *clickhouse.Writer) *chTradeSink { return &chTradeSink{writer: w} }

func (s *chTradeSink) Name() string { return "clickhouse" }

func (s *chTradeSink) HandleExecReport(ctx context.Context, report engine.ExecReport) error {
	if report.Trade == nil {
		return nil
	}
	return s.writer.InsertTrades(ctx, []clickhouse.TradeTick{toCHTradeTick(report)})
}

func reportTime(report engine.ExecReport) time.Time {
	ts := report.Timestamp
	if ts == 0 {
		return time.Now()
	}
	return time.Unix(0, ts)
}

func tradeTime(report engine.ExecReport) time.Time {
	if report.Trade == nil || report.Trade.Timestamp == 0 {
		return reportTime(report)
	}
	return time.Unix(0, report.Trade.Timestamp)
}

func toPGExecEvent(report engine.ExecReport) postgres.ExecEvent {
	var tradeID uint64
	if report.Trade != nil {
		tradeID = report.Trade.ID
	}
	return postgres.ExecEvent{
		ExecID:        report.ExecID,
		Seq:           report.Seq,
		OrderID:       report.OrderID,
		ClientOrderID: report.ClientOrderID,
		AccountID:     report.AccountID,
		Symbol:        report.Symbol,
		Side:          int8(report.Side),
		ExecType:      string(report.ExecType),
		Status:        int8(report.Status),
		LastPx:        report.LastPx,
		LastQty:       report.LastQty,
		CumQty:        report.CumQty,
		LeavesQty:     report.LeavesQty,
		Liquidity:     string(report.Liquidity),
		TradeID:       tradeID,
		Reason:        report.Reason,
		Venue:         report.Venue,
		ReportedAt:    reportTime(report),
	}
}

func toPGTrade(report engine.ExecReport) postgres.Trade {
	t := report.Trade
	return postgres.Trade{
		ID:           t.ID,
		Symbol:       report.Symbol,
		Price:        t.Price,
		Quantity:     t.Quantity,
		BuyOrderID:   t.BuyOrderID,
		SellOrderID:  t.SellOrderID,
		MakerOrderID: t.MakerOrder,
		TradedAt:     tradeTime(report),
	}
}

func toPGOrder(report engine.ExecReport) postgres.Order {
	ts := reportTime(report)
	return postgres.Order{
		ID:            report.OrderID,
		ClientOrderID: report.ClientOrderID,
		AccountID:     report.AccountID,
		Symbol:        report.Symbol,
		Side:          int8(report.Side),
		OrderType:     string(report.OrderType),
		TimeInForce:   string(report.TIF),
		Price:         report.OrderPx,
		Quantity:      report.Quantity,
		Leaves:        report.Leaves,
		Filled:        report.Filled,
		Status:        int8(report.Status),
		CreatedAt:     ts,
		UpdatedAt:     ts,
	}
}

func toCHTradeTick(report engine.ExecReport) clickhouse.TradeTick {
	t := report.Trade
	return clickhouse.TradeTick{
		TS:          tradeTime(report),
		Symbol:      report.Symbol,
		TradeID:     t.ID,
		Price:       t.Price,
		Quantity:    t.Quantity,
		BuyOrderID:  t.BuyOrderID,
		SellOrderID: t.SellOrderID,
	}
}

var (
	_ pipeline.ExecReportSink = (*pgExecSink)(nil)
	_ pipeline.ExecReportSink = (*chTradeSink)(nil)
)

// pgRiskSink persists risk events. It implements risk.Sink and is wrapped by
// risk.AsyncSink, so a slow database never blocks the matching goroutine.
type pgRiskSink struct {
	writer *postgres.Writer
}

func newPGRiskSink(w *postgres.Writer) *pgRiskSink { return &pgRiskSink{writer: w} }

func (s *pgRiskSink) Record(ev risk.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ts := time.UnixMilli(ev.TS)
	if ev.TS == 0 {
		ts = time.Now()
	}
	err := s.writer.InsertRiskEvent(ctx, postgres.RiskEvent{
		TS:        ts,
		AccountID: ev.AccountID,
		Symbol:    ev.Symbol,
		Strategy:  ev.Strategy,
		OrderID:   ev.OrderID,
		Code:      ev.Code,
		Action:    string(ev.Action),
		Message:   ev.Message,
	})
	if err != nil {
		log.Printf("persist risk event failed: %v", err)
	}
}

// riskEventLister adapts the Postgres reader to the HTTP endpoint contract. A
// nil writer yields a nil lister, which the endpoint renders as an empty page.
func riskEventLister(w *postgres.Writer) tradinghttp.RiskEventLister {
	if w == nil {
		return nil
	}
	return func(ctx context.Context, f tradinghttp.RiskEventFilter) ([]tradinghttp.RiskEventRecord, error) {
		rows, err := w.ListRiskEvents(ctx, postgres.RiskEventQuery{
			Limit:     f.Limit,
			AccountID: f.AccountID,
			Symbol:    f.Symbol,
			Code:      f.Code,
			BeforeID:  f.BeforeID,
		})
		if err != nil {
			return nil, err
		}

		out := make([]tradinghttp.RiskEventRecord, 0, len(rows))
		for _, row := range rows {
			out = append(out, tradinghttp.RiskEventRecord{
				ID:        row.ID,
				TS:        row.TS.UnixMilli(),
				AccountID: row.AccountID,
				Symbol:    row.Symbol,
				Strategy:  row.Strategy,
				OrderID:   row.OrderID,
				Code:      row.Code,
				Action:    row.Action,
				Message:   row.Message,
			})
		}
		return out, nil
	}
}
