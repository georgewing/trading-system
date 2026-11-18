package binanceexec

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"trading-system/internal/hft/engine"
)

func reportConfig() Config {
	return Config{
		PriceScale:    testScale,
		QuantityScale: testScale,
		AccountID:     9,
	}
}

func TestParseExecutionReportNew(t *testing.T) {
	raw := []byte(`{
		"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
		"S":"BUY","o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
		"x":"NEW","X":"NEW","r":"NONE","i":42,
		"l":"0.00000000","z":"0.00000000","L":"0.00000000",
		"T":1700000000150,"t":-1,"m":false
	}`)

	report, err := ParseExecutionReport(raw, reportConfig())
	if err != nil {
		t.Fatalf("ParseExecutionReport() error = %v", err)
	}

	want := engine.ExecReport{
		OrderID:       1001,
		ClientOrderID: "1001",
		AccountID:     9,
		Symbol:        "BTCUSDT",
		Side:          engine.SideBuy,
		ExecType:      engine.ExecTypeNew,
		Status:        engine.OrderStatusNew,
		Leaves:        200_000_000,
		LeavesQty:     200_000_000,
		OrderPx:       2_500_000_000_000,
		OrderType:     engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Quantity:      "2.00000000",
		Venue:         "binance",
		Timestamp:     1_700_000_000_150 * int64(time.Millisecond),
	}
	if report != want {
		t.Fatalf("ParseExecutionReport() = %+v, want %+v", report, want)
	}
	if report.Trade != nil {
		t.Fatalf("Trade = %+v, want nil for a NEW report", report.Trade)
	}
}

func TestParseExecutionReportTrade(t *testing.T) {
	raw := []byte(`{
		"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
		"S":"BUY","o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
		"x":"TRADE","X":"PARTIALLY_FILLED","r":"NONE","i":42,
		"l":"0.50000000","z":"0.50000000","L":"24999.50000000",
		"T":1700000000150,"t":77,"m":true
	}`)

	report, err := ParseExecutionReport(raw, reportConfig())
	if err != nil {
		t.Fatalf("ParseExecutionReport() error = %v", err)
	}

	if report.ExecID != 77 {
		t.Fatalf("ExecID = %d, want the trade id 77", report.ExecID)
	}
	if report.OrderID != 1001 || report.ClientOrderID != "1001" {
		t.Fatalf("order ids = %d/%q, want 1001/1001", report.OrderID, report.ClientOrderID)
	}
	if report.ExecType != engine.ExecTypeTrade || report.Status != engine.OrderStatusPartiallyFilled {
		t.Fatalf("exec/status = %s/%v", report.ExecType, report.Status)
	}
	if report.Liquidity != engine.LiquidityMaker {
		t.Fatalf("Liquidity = %q, want MAKER for a BUY with m=true", report.Liquidity)
	}
	if report.LastPx != 2_499_950_000_000 || report.LastQty != 50_000_000 {
		t.Fatalf("last = %d/%d", report.LastPx, report.LastQty)
	}
	if report.CumQty != 50_000_000 || report.Filled != 50_000_000 {
		t.Fatalf("cum/filled = %d/%d", report.CumQty, report.Filled)
	}
	if report.LeavesQty != 150_000_000 || report.Leaves != 150_000_000 {
		t.Fatalf("leaves = %d/%d", report.LeavesQty, report.Leaves)
	}

	trade := report.Trade
	if trade == nil {
		t.Fatal("Trade = nil, want the fill")
	}
	wantTrade := engine.Trade{
		ID:         77,
		Symbol:     "BTCUSDT",
		Price:      2_499_950_000_000,
		Quantity:   50_000_000,
		BuyOrderID: 1001,
		Timestamp:  1_700_000_000_150 * int64(time.Millisecond),
	}
	if *trade != wantTrade {
		t.Fatalf("Trade = %+v, want %+v", *trade, wantTrade)
	}
}

func TestParseExecutionReportLiquidityIsSideAware(t *testing.T) {
	tests := []struct {
		name         string
		side         string
		buyerIsMaker bool
		want         engine.Liquidity
	}{
		{name: "buy maker", side: "BUY", buyerIsMaker: true, want: engine.LiquidityMaker},
		{name: "buy taker", side: "BUY", buyerIsMaker: false, want: engine.LiquidityTaker},
		{name: "sell maker", side: "SELL", buyerIsMaker: false, want: engine.LiquidityMaker},
		{name: "sell taker", side: "SELL", buyerIsMaker: true, want: engine.LiquidityTaker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Appendf(nil, `{
				"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
				"S":%q,"o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
				"x":"TRADE","X":"PARTIALLY_FILLED","r":"NONE","i":42,
				"l":"0.50000000","z":"0.50000000","L":"24999.50000000",
				"T":1700000000150,"t":77,"m":%t
			}`, tt.side, tt.buyerIsMaker)

			report, err := ParseExecutionReport(raw, reportConfig())
			if err != nil {
				t.Fatalf("ParseExecutionReport() error = %v", err)
			}
			if report.Liquidity != tt.want {
				t.Fatalf("Liquidity = %q, want %q", report.Liquidity, tt.want)
			}

			if tt.side == "SELL" {
				if report.Trade == nil || report.Trade.SellOrderID != 1001 || report.Trade.BuyOrderID != 0 {
					t.Fatalf("sell trade ids = %+v, want SellOrderID=1001", report.Trade)
				}
			}
		})
	}
}

func TestParseExecutionReportCanceled(t *testing.T) {
	raw := []byte(`{
		"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
		"S":"BUY","o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
		"x":"CANCELED","X":"CANCELED","r":"NONE","i":42,
		"l":"0.00000000","z":"1.00000000","L":"0.00000000",
		"T":1700000000150,"t":-1,"m":false
	}`)

	report, err := ParseExecutionReport(raw, reportConfig())
	if err != nil {
		t.Fatalf("ParseExecutionReport() error = %v", err)
	}

	if report.ExecType != engine.ExecTypeCanceled || report.Status != engine.OrderStatusCanceled {
		t.Fatalf("exec/status = %s/%v, want CANCELED/Canceled", report.ExecType, report.Status)
	}
	if report.CumQty != 100_000_000 || report.LeavesQty != 100_000_000 {
		t.Fatalf("cum/leaves = %d/%d, want 100000000/100000000", report.CumQty, report.LeavesQty)
	}
	if report.Trade != nil {
		t.Fatalf("Trade = %+v, want nil for a cancel", report.Trade)
	}
	if report.Liquidity != "" {
		t.Fatalf("Liquidity = %q, want empty for a cancel", report.Liquidity)
	}
}

func TestParseExecutionReportRejected(t *testing.T) {
	raw := []byte(`{
		"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
		"S":"SELL","o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
		"x":"REJECTED","X":"REJECTED","r":"INSUFFICIENT_BALANCE","i":42,
		"l":"0.00000000","z":"0.00000000","L":"0.00000000",
		"T":1700000000150,"t":-1,"m":false
	}`)

	report, err := ParseExecutionReport(raw, reportConfig())
	if err != nil {
		t.Fatalf("ParseExecutionReport() error = %v", err)
	}

	if report.ExecType != engine.ExecTypeRejected || report.Status != engine.OrderStatusRejected {
		t.Fatalf("exec/status = %s/%v, want REJECTED/Rejected", report.ExecType, report.Status)
	}
	if report.Reason != "INSUFFICIENT_BALANCE" {
		t.Fatalf("Reason = %q, want INSUFFICIENT_BALANCE", report.Reason)
	}
	if report.Side != engine.SideSell {
		t.Fatalf("Side = %v, want SELL", report.Side)
	}
}

func TestParseExecutionReportExpired(t *testing.T) {
	raw := []byte(`{
		"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
		"S":"BUY","o":"LIMIT","f":"IOC","q":"2.00000000","p":"25000.00000000",
		"x":"EXPIRED","X":"EXPIRED","r":"NONE","i":42,
		"l":"0.00000000","z":"0.00000000","L":"0.00000000",
		"T":1700000000150,"t":-1,"m":false
	}`)

	report, err := ParseExecutionReport(raw, reportConfig())
	if err != nil {
		t.Fatalf("ParseExecutionReport() error = %v", err)
	}

	if report.ExecType != ExecTypeExpired {
		t.Fatalf("ExecType = %q, want %q", report.ExecType, ExecTypeExpired)
	}
	if report.Status != engine.OrderStatusCanceled {
		t.Fatalf("Status = %v, want Canceled for EXPIRED", report.Status)
	}
	if report.Reason != "EXPIRED" {
		t.Fatalf("Reason = %q, want EXPIRED", report.Reason)
	}
}

func TestParseExecutionReportOrderIDFallback(t *testing.T) {
	tests := []struct {
		name          string
		clientOrderID string
		wantOrderID   uint64
	}{
		{name: "numeric client id", clientOrderID: "1001", wantOrderID: 1001},
		{name: "non numeric client id", clientOrderID: "client-7", wantOrderID: 42},
		{name: "empty client id", clientOrderID: "", wantOrderID: 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Appendf(nil, `{
				"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":%q,
				"S":"BUY","o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
				"x":"NEW","X":"NEW","r":"NONE","i":42,
				"l":"0","z":"0","L":"0","T":1700000000150,"t":-1,"m":false
			}`, tt.clientOrderID)

			report, err := ParseExecutionReport(raw, reportConfig())
			if err != nil {
				t.Fatalf("ParseExecutionReport() error = %v", err)
			}
			if report.OrderID != tt.wantOrderID {
				t.Fatalf("OrderID = %d, want %d", report.OrderID, tt.wantOrderID)
			}
		})
	}
}

func TestParseExecutionReportTimestampFallback(t *testing.T) {
	raw := []byte(`{
		"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
		"S":"BUY","o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
		"x":"NEW","X":"NEW","r":"NONE","i":42,
		"l":"0","z":"0","L":"0","T":0,"t":-1,"m":false
	}`)

	report, err := ParseExecutionReport(raw, reportConfig())
	if err != nil {
		t.Fatalf("ParseExecutionReport() error = %v", err)
	}
	if want := int64(1_700_000_000_200) * int64(time.Millisecond); report.Timestamp != want {
		t.Fatalf("Timestamp = %d, want event time %d", report.Timestamp, want)
	}
}

func TestParseExecutionReportSkipsNonExecutionEvents(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{
			name: "balance update",
			raw:  `{"e":"outboundAccountPosition","E":1700000000000,"u":1700000000000,"B":[{"a":"BTC","f":"1.0","l":"1.0"}]}`,
			want: ErrNotExecutionReport,
		},
		{
			name: "unknown event",
			raw:  `{"foo":"bar"}`,
			want: ErrNotExecutionReport,
		},
		{
			name: "listen key expired",
			raw:  `{"e":"listenKeyExpired","E":1700000000000}`,
			want: ErrListenKeyExpired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseExecutionReport([]byte(tt.raw), reportConfig())
			if !errors.Is(err, tt.want) {
				t.Fatalf("ParseExecutionReport() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParseExecutionReportInvalidJSON(t *testing.T) {
	for _, raw := range []string{"", "{", "not json", `{"e":"executionReport",`} {
		if _, err := ParseExecutionReport([]byte(raw), reportConfig()); err == nil {
			t.Fatalf("ParseExecutionReport(%q) error = nil, want a decode error", raw)
		}
	}
}

func TestParseExecutionReportRejectsUnsupportedValues(t *testing.T) {
	tests := []struct {
		name  string
		spot  string
		value string
	}{
		{name: "side", spot: `"S":"BUY"`, value: `"S":"HOLD"`},
		{name: "exec type", spot: `"x":"NEW"`, value: `"x":"REPLACED"`},
		{name: "status", spot: `"X":"NEW"`, value: `"X":"PENDING_CANCEL"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := `{
				"e":"executionReport","E":1700000000200,"s":"BTCUSDT","c":"1001",
				"S":"BUY","o":"LIMIT","f":"GTC","q":"2.00000000","p":"25000.00000000",
				"x":"NEW","X":"NEW","r":"NONE","i":42,
				"l":"0","z":"0","L":"0","T":1700000000150,"t":-1,"m":false
			}`
			frame = strings.Replace(frame, tt.spot, tt.value, 1)

			if _, err := ParseExecutionReport([]byte(frame), reportConfig()); err == nil {
				t.Fatalf("ParseExecutionReport() error = nil, want a mapping error")
			}
		})
	}
}

func TestParseExecutionReportRejectsBadInput(t *testing.T) {
	t.Run("missing symbol", func(t *testing.T) {
		raw := []byte(`{"e":"executionReport","s":"","S":"BUY","o":"LIMIT","f":"GTC","q":"1","p":"1","x":"NEW","X":"NEW","i":1}`)
		if _, err := ParseExecutionReport(raw, reportConfig()); err == nil {
			t.Fatal("ParseExecutionReport() error = nil, want a symbol error")
		}
	})

	t.Run("excess quantity precision", func(t *testing.T) {
		raw := []byte(`{
			"e":"executionReport","s":"BTCUSDT","c":"1001","S":"BUY","o":"LIMIT","f":"GTC",
			"q":"1.000000001","p":"25000.00000000","x":"NEW","X":"NEW","i":42,
			"l":"0","z":"0","L":"0","T":1,"t":-1,"m":false
		}`)
		_, err := ParseExecutionReport(raw, reportConfig())
		if !errors.Is(err, engine.ErrQuantityPrecision) {
			t.Fatalf("ParseExecutionReport() error = %v, want ErrQuantityPrecision", err)
		}
	})

	t.Run("invalid scales", func(t *testing.T) {
		cfg := reportConfig()
		cfg.PriceScale = 0
		raw := []byte(`{"e":"executionReport","s":"BTCUSDT","S":"BUY","o":"LIMIT","f":"GTC","q":"1","p":"1","x":"NEW","X":"NEW","i":1}`)
		if _, err := ParseExecutionReport(raw, cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("ParseExecutionReport() error = %v, want ErrInvalidConfig", err)
		}
	})
}
