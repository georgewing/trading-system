// Package execution defines venue abstractions for order submission and cancel.
// It is deliberately independent of app wiring, configuration and pipeline code.
package execution

import (
	"context"

	"trading-system/internal/hft/engine"
)

// CancelRequest identifies an order to cancel at a venue.
type CancelRequest struct {
	OrderID       uint64
	ClientOrderID string
	Symbol        string
	Reason        string
}

// Venue submits and cancels orders against a single execution destination.
// Submit returns the detailed matching result for the order.
type Venue interface {
	Name() string
	Submit(ctx context.Context, order engine.Order) (engine.SubmitResult, error)
	Cancel(ctx context.Context, req CancelRequest) error
}

// Matcher is the narrow subset of the matching engine used by venues.
type Matcher interface {
	SubmitOrderDetailed(ctx context.Context, order engine.Order) (engine.SubmitResult, error)
}

// Compile-time check that the engine matcher satisfies the narrow interface.
var _ Matcher = (*engine.Matcher)(nil)
