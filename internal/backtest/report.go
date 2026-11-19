package backtest

import (
	"encoding/json"
	"fmt"
	"io"
)

// Report is the machine-readable result of one backtest.
type Report struct {
	Strategy string   `json:"strategy"`
	Symbols  []string `json:"symbols"`
	Events   int      `json:"events"`

	StartTS int64 `json:"start_ts"`
	EndTS   int64 `json:"end_ts"`

	InitialCash   int64 `json:"initial_cash"`
	FinalEquity   int64 `json:"final_equity"`
	RealizedPnL   int64 `json:"realized_pnl"`
	UnrealizedPnL int64 `json:"unrealized_pnl"`
	Fees          int64 `json:"fees"`

	MaxDrawdownBps int64   `json:"max_drawdown_bps"`
	Sharpe         float64 `json:"sharpe"`

	OrdersSubmitted int   `json:"orders_submitted"`
	OrdersFilled    int   `json:"orders_filled"`
	OrdersCanceled  int   `json:"orders_canceled"`
	Fills           int   `json:"fills"`
	FillRateBps     int64 `json:"fill_rate_bps"`
	WinRateBps      int64 `json:"win_rate_bps"`
	TurnoverBps     int64 `json:"turnover_bps"`

	MaxGrossExposure int64 `json:"max_gross_exposure"`

	Assumptions []string      `json:"assumptions"`
	EquityCurve []EquityPoint `json:"equity_curve,omitempty"`
}

// ReportInput gathers everything BuildReport needs.
type ReportInput struct {
	Strategy    string
	Symbols     []string
	Events      int
	BarMillis   int64
	Portfolio   *Portfolio
	Stats       Stats
	Equity      []EquityPoint
	Assumptions []string
}

// DefaultAssumptions documents what the simulator does not model. Reports carry
// them so a reader cannot mistake a backtest for a live-trading guarantee.
func DefaultAssumptions() []string {
	return []string{
		"fills come from public trades and book moves, not from other participants' order flow",
		"queue position at the touch is a probability, not a real queue",
		"submission-to-match latency is zero",
		"no exchange rejects, throttles or maintenance windows are modelled",
		"fees are charged in notional units and rounded down",
	}
}

// BuildReport derives report metrics from a finished simulation.
func BuildReport(in ReportInput) Report {
	r := Report{
		Strategy:    in.Strategy,
		Symbols:     append([]string(nil), in.Symbols...),
		Events:      in.Events,
		Assumptions: in.Assumptions,
	}
	if len(r.Assumptions) == 0 {
		r.Assumptions = DefaultAssumptions()
	}

	if n := len(in.Equity); n > 0 {
		r.StartTS = in.Equity[0].TS
		r.EndTS = in.Equity[n-1].TS
	}

	if in.Portfolio != nil {
		r.InitialCash = in.Portfolio.InitialCash()
		r.FinalEquity = in.Portfolio.Equity()
		r.RealizedPnL = in.Portfolio.Realized()
		r.UnrealizedPnL = in.Portfolio.Unrealized()
		r.Fees = in.Portfolio.Fees()
		r.MaxGrossExposure = in.Portfolio.MaxGrossExposure()

		wins, total := in.Portfolio.ClosedTrades()
		if total > 0 {
			r.WinRateBps = int64(wins) * 10000 / int64(total)
		}

		if avg := AverageEquity(in.Equity, in.BarMillis); avg > 0 {
			r.TurnoverBps = int64(float64(in.Portfolio.Turnover()) * 10000 / avg)
		}
	}

	if in.Stats.Submitted > 0 {
		r.FillRateBps = int64(in.Stats.FilledOrders) * 10000 / int64(in.Stats.Submitted)
	}

	r.MaxDrawdownBps = MaxDrawdownBps(in.Equity)
	r.Sharpe = Sharpe(in.Equity, in.BarMillis)
	r.OrdersSubmitted = in.Stats.Submitted
	r.OrdersFilled = in.Stats.FilledOrders
	r.OrdersCanceled = in.Stats.CanceledOrders
	r.Fills = in.Stats.Fills
	r.EquityCurve = Bars(in.Equity, in.BarMillis)

	return r
}

// WriteJSON writes the indented JSON form.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteMarkdown writes a human-readable summary table.
func (r Report) WriteMarkdown(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "# Backtest Report: %s\n\n", r.Strategy); err != nil {
		return err
	}

	rows := [][2]string{
		{"symbols", fmt.Sprint(r.Symbols)},
		{"events", fmt.Sprint(r.Events)},
		{"start_ts", fmt.Sprint(r.StartTS)},
		{"end_ts", fmt.Sprint(r.EndTS)},
		{"initial_cash", fmt.Sprint(r.InitialCash)},
		{"final_equity", fmt.Sprint(r.FinalEquity)},
		{"realized_pnl", fmt.Sprint(r.RealizedPnL)},
		{"unrealized_pnl", fmt.Sprint(r.UnrealizedPnL)},
		{"fees", fmt.Sprint(r.Fees)},
		{"max_drawdown", fmt.Sprintf("%.2f%%", float64(r.MaxDrawdownBps)/100)},
		{"sharpe", fmt.Sprintf("%.2f", r.Sharpe)},
		{"orders_submitted", fmt.Sprint(r.OrdersSubmitted)},
		{"orders_filled", fmt.Sprint(r.OrdersFilled)},
		{"orders_canceled", fmt.Sprint(r.OrdersCanceled)},
		{"fills", fmt.Sprint(r.Fills)},
		{"fill_rate", fmt.Sprintf("%.2f%%", float64(r.FillRateBps)/100)},
		{"win_rate", fmt.Sprintf("%.2f%%", float64(r.WinRateBps)/100)},
		{"turnover", fmt.Sprintf("%.2f%%", float64(r.TurnoverBps)/100)},
		{"max_gross_exposure", fmt.Sprint(r.MaxGrossExposure)},
	}

	if _, err := fmt.Fprintln(w, "| metric | value |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | --- |"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "| %s | %s |\n", row[0], row[1]); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(w, "\n## Assumptions"); err != nil {
		return err
	}
	for _, a := range r.Assumptions {
		if _, err := fmt.Fprintf(w, "- %s\n", a); err != nil {
			return err
		}
	}
	return nil
}
