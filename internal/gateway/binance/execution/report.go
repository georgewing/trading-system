package binanceexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"trading-system/internal/hft/engine"
)

// ExecTypeExpired is emitted for Binance's EXPIRED execution type. The engine
// package has no constant for it yet; the value is part of this gateway's
// contract so consumers can match reports from expired orders.
const ExecTypeExpired engine.ExecType = "EXPIRED"

// execEvent is the subset of a Binance user data stream executionReport frame
// this gateway consumes.
//
// Every key is declared explicitly: Binance mixes cases inside one object
// ("L" last price vs "l" last quantity, "T" transaction time vs "t" trade id)
// and encoding/json would otherwise fall back to case-insensitive matching.
type execEvent struct {
	EventType     string `json:"e"`
	EventTime     int64  `json:"E"`
	Symbol        string `json:"s"`
	ClientOrderID string `json:"c"`
	Side          string `json:"S"`
	OrderType     string `json:"o"`
	TimeInForce   string `json:"f"`
	Quantity      string `json:"q"`
	Price         string `json:"p"`
	ExecType      string `json:"x"`
	Status        string `json:"X"`
	RejectReason  string `json:"r"`
	OrderID       uint64 `json:"i"`
	LastQty       string `json:"l"`
	CumQty        string `json:"z"`
	LastPx        string `json:"L"`
	TradeID       int64  `json:"t"`
	TransactTime  int64  `json:"T"`
	IsBuyerMaker  bool   `json:"m"`
}

// ParseExecutionReport converts one user data stream frame into an
// engine.ExecReport. It is pure and exported so the mapping can be tested
// without a websocket connection.
//
// Returned sentinels:
//   - ErrNotExecutionReport for frames that are not executionReport events,
//     such as outboundAccountPosition or subscription acknowledgements;
//   - ErrListenKeyExpired for Binance's listenKeyExpired event;
//   - ErrInvalidConfig when cfg scales are not positive powers of ten.
//
// Frames whose side, execution type or status this gateway cannot represent
// are rejected instead of being coerced, so the caller can log and count them.
//
// Report correlation: OrderID is taken from the client order id when it parses
// as a uint64 (Submit defaults newClientOrderId to the decimal engine order id),
// otherwise from Binance's numeric order id. When a caller supplies a
// non-numeric ClientOrderID, OrderID therefore carries the venue id and cannot
// be mapped back to the engine order without an external mapping.
//
// Liquidity is derived from the side and "m" (is the buyer the maker): a BUY
// fill with m=true and a SELL fill with m=false are both maker fills.
func ParseExecutionReport(raw []byte, cfg Config) (engine.ExecReport, error) {
	if !isPowerOfTen(cfg.PriceScale) || !isPowerOfTen(cfg.QuantityScale) {
		return engine.ExecReport{}, fmt.Errorf("%w: PriceScale and QuantityScale must be positive powers of ten",
			ErrInvalidConfig)
	}

	var event execEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return engine.ExecReport{}, fmt.Errorf("binance: decode user stream frame: %w", err)
	}

	switch event.EventType {
	case "executionReport":
	case "listenKeyExpired":
		return engine.ExecReport{}, ErrListenKeyExpired
	default:
		return engine.ExecReport{}, fmt.Errorf("%w: event %q", ErrNotExecutionReport, event.EventType)
	}

	if event.Symbol == "" {
		return engine.ExecReport{}, errors.New("binance: execution report without symbol")
	}

	side, err := mapSide(event.Side)
	if err != nil {
		return engine.ExecReport{}, err
	}
	execType, err := mapExecType(event.ExecType)
	if err != nil {
		return engine.ExecReport{}, err
	}
	status, err := mapOrderStatus(event.Status)
	if err != nil {
		return engine.ExecReport{}, err
	}

	quantity, err := parseDecimal("order quantity", event.Quantity, cfg.QuantityScale)
	if err != nil {
		return engine.ExecReport{}, err
	}
	lastQty, err := parseDecimal("last quantity", event.LastQty, cfg.QuantityScale)
	if err != nil {
		return engine.ExecReport{}, err
	}
	cumQty, err := parseDecimal("cumulative quantity", event.CumQty, cfg.QuantityScale)
	if err != nil {
		return engine.ExecReport{}, err
	}
	lastPx, err := parseDecimal("last price", event.LastPx, cfg.PriceScale)
	if err != nil {
		return engine.ExecReport{}, err
	}
	orderPx, err := parseDecimal("order price", event.Price, cfg.PriceScale)
	if err != nil {
		return engine.ExecReport{}, err
	}

	leaves := quantity - cumQty
	if leaves < 0 {
		leaves = 0
	}

	reason := strings.TrimSpace(event.RejectReason)
	if reason == "NONE" {
		reason = ""
	}
	if reason == "" && (event.Status == "EXPIRED" || event.Status == "EXPIRED_IN_MATCH") {
		reason = event.Status
	}

	timestamp := event.TransactTime
	if timestamp <= 0 {
		timestamp = event.EventTime
	}
	tsNanos := timestamp * int64(time.Millisecond)

	orderID := event.OrderID
	if id, parseErr := strconv.ParseUint(strings.TrimSpace(event.ClientOrderID), 10, 64); parseErr == nil {
		orderID = id
	}

	report := engine.ExecReport{
		OrderID:       orderID,
		ClientOrderID: event.ClientOrderID,
		AccountID:     cfg.AccountID,
		Symbol:        event.Symbol,
		Side:          side,
		ExecType:      execType,
		Status:        status,
		Filled:        cumQty,
		Leaves:        leaves,
		LastPx:        lastPx,
		LastQty:       lastQty,
		CumQty:        cumQty,
		LeavesQty:     leaves,
		OrderPx:       orderPx,
		OrderType:     engine.OrderType(event.OrderType),
		TIF:           engine.TimeInForce(event.TimeInForce),
		Quantity:      event.Quantity,
		Reason:        reason,
		Venue:         VenueName,
		Timestamp:     tsNanos,
	}

	if execType == engine.ExecTypeTrade {
		var tradeID uint64
		if event.TradeID > 0 {
			tradeID = uint64(event.TradeID)
		}
		report.ExecID = tradeID
		report.Liquidity = liquidityFor(side, event.IsBuyerMaker)

		if lastQty > 0 {
			trade := &engine.Trade{
				ID:        tradeID,
				Symbol:    event.Symbol,
				Price:     lastPx,
				Quantity:  lastQty,
				Timestamp: tsNanos,
			}
			if side == engine.SideBuy {
				trade.BuyOrderID = orderID
			} else {
				trade.SellOrderID = orderID
			}
			report.Trade = trade
		}
	}

	return report, nil
}

func mapSide(raw string) (engine.Side, error) {
	switch raw {
	case "BUY":
		return engine.SideBuy, nil
	case "SELL":
		return engine.SideSell, nil
	default:
		return 0, fmt.Errorf("binance: unsupported side %q", raw)
	}
}

func mapExecType(raw string) (engine.ExecType, error) {
	switch raw {
	case "NEW":
		return engine.ExecTypeNew, nil
	case "TRADE":
		return engine.ExecTypeTrade, nil
	case "CANCELED":
		return engine.ExecTypeCanceled, nil
	case "REJECTED":
		return engine.ExecTypeRejected, nil
	case "EXPIRED":
		return ExecTypeExpired, nil
	default:
		return "", fmt.Errorf("binance: unsupported execution type %q", raw)
	}
}

// liquidityFor applies the side-aware meaning of Binance's "m" flag, which
// reports whether the buyer was the maker.
func liquidityFor(side engine.Side, buyerIsMaker bool) engine.Liquidity {
	if (side == engine.SideBuy) == buyerIsMaker {
		return engine.LiquidityMaker
	}
	return engine.LiquidityTaker
}
