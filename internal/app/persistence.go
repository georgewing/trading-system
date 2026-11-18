package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"trading-system/internal/hft/engine"
	"trading-system/internal/pipeline"
	"trading-system/internal/storage/clickhouse"
	"trading-system/internal/storage/postgres"
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
		timeout := time.Duration(cfg.Storage.ClickHouseTimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
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
