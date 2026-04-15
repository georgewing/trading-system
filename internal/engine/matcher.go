package engine

import (
	"context"
	"github.com/go-kit/kit/endpoint"
)

type Matcher struct {
	ob *OrderBook
}

func NewMatcher(ob *OrderBook) *Matcher {
	return &Matcher{ob: ob}
}

func (m *Matcher) Match(ctx context.Context, order Order) ([]Fill, error) {
	m.ob.AddOrder(ctx, order)
	// 撮合逻辑（原仓库核心，简化演示，生产可扩展）
	var fills []Fill
	// ... 真实撮合算法（L2/L3 深度）
	return fills, nil
}

// Go-kit Endpoint
func MakeMatchEndpoint(m *Matcher) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(Order)
		return m.Match(ctx, req)
	}
}
