package strategy

import (
	"errors"
	"fmt"

	"trading-system/internal/hft/engine"
)

// MarketMaker quotes a symmetric two-sided market around the mid price and
// re-quotes when the mid moves by more than a threshold.
//
// Parameters:
//
//	symbol           traded symbol (required)
//	half_spread_bps  half spread in basis points (default 5)
//	order_qty        quote size in lots (required)
//	inventory_limit  absolute position cap in lots; 0 disables the cap
//	min_requote_bps  mid move that triggers a re-quote (default 1)
//
// The strategy only quotes the side that reduces inventory when the position is
// at the cap, so it never grows exposure past the limit on its own.
type MarketMaker struct {
	symbol         string
	halfSpreadBps  int64
	orderQty       int64
	inventoryLimit int64
	minRequoteBps  int64

	lastQuoteMid int64
	quoted       bool
}

// NewMarketMaker builds an unconfigured market maker.
func NewMarketMaker() *MarketMaker { return &MarketMaker{} }

func (m *MarketMaker) Name() string { return "market_maker" }

func (m *MarketMaker) Init(_ Context, params map[string]string) error {
	var err error

	m.symbol = stringParam(params, "symbol", "")
	if m.symbol == "" {
		return errors.New("market_maker: symbol is required")
	}
	if m.halfSpreadBps, err = intParam(params, "half_spread_bps", 5); err != nil {
		return err
	}
	if m.orderQty, err = intParam(params, "order_qty", 0); err != nil {
		return err
	}
	if m.orderQty <= 0 {
		return errors.New("market_maker: order_qty must be positive")
	}
	if m.inventoryLimit, err = intParam(params, "inventory_limit", 0); err != nil {
		return err
	}
	if m.inventoryLimit < 0 {
		return errors.New("market_maker: inventory_limit must not be negative")
	}
	if m.minRequoteBps, err = intParam(params, "min_requote_bps", 1); err != nil {
		return err
	}
	if m.halfSpreadBps < 0 || m.minRequoteBps < 0 {
		return errors.New("market_maker: bps parameters must not be negative")
	}
	return nil
}

// OnBook is intentionally unused. Quoting is timer-driven so a busy book does
// not turn into a cancel/replace storm.
func (m *MarketMaker) OnBook(Context, BookView) error { return nil }

func (m *MarketMaker) OnTrade(Context, TradeView) error { return nil }

func (m *MarketMaker) OnTimer(ctx Context, _ int64) error {
	book, ok := ctx.Book(m.symbol)
	if !ok {
		return nil
	}
	mid, ok := book.Mid()
	if !ok || mid <= 0 {
		return nil
	}

	if m.quoted && !m.shouldRequote(mid) {
		return nil
	}

	for _, id := range ctx.OpenOrders(m.symbol) {
		if err := ctx.Cancel(id); err != nil {
			return fmt.Errorf("market_maker: cancel %d: %w", id, err)
		}
	}

	spread := mid * m.halfSpreadBps / 10000
	if spread < 1 {
		spread = 1
	}
	bidPrice := mid - spread
	askPrice := mid + spread
	if bidPrice <= 0 {
		bidPrice = 1
	}
	if askPrice <= bidPrice {
		askPrice = bidPrice + 1
	}

	pos := ctx.Position(m.symbol)
	canBuy := m.inventoryLimit <= 0 || pos < m.inventoryLimit
	canSell := m.inventoryLimit <= 0 || pos > -m.inventoryLimit

	if canBuy {
		if _, err := ctx.Submit(OrderRequest{
			Symbol: m.symbol,
			Side:   engine.SideBuy,
			Type:   engine.OrderTypeLimit,
			TIF:    engine.TimeInForceGTC,
			Price:  bidPrice,
			Qty:    m.orderQty,
			Tag:    "mm-bid",
		}); err != nil {
			return fmt.Errorf("market_maker: submit bid: %w", err)
		}
	}
	if canSell {
		if _, err := ctx.Submit(OrderRequest{
			Symbol: m.symbol,
			Side:   engine.SideSell,
			Type:   engine.OrderTypeLimit,
			TIF:    engine.TimeInForceGTC,
			Price:  askPrice,
			Qty:    m.orderQty,
			Tag:    "mm-ask",
		}); err != nil {
			return fmt.Errorf("market_maker: submit ask: %w", err)
		}
	}

	m.lastQuoteMid = mid
	m.quoted = true
	return nil
}

func (m *MarketMaker) Stop(ctx Context) error {
	for _, id := range ctx.OpenOrders(m.symbol) {
		if err := ctx.Cancel(id); err != nil {
			return fmt.Errorf("market_maker: cancel %d: %w", id, err)
		}
	}
	return nil
}

func (m *MarketMaker) shouldRequote(mid int64) bool {
	if m.lastQuoteMid <= 0 {
		return true
	}
	delta := mid - m.lastQuoteMid
	if delta < 0 {
		delta = -delta
	}
	return delta*10000/m.lastQuoteMid >= m.minRequoteBps
}

var _ Strategy = (*MarketMaker)(nil)
