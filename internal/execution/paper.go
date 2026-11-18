package execution

import (
	"context"
	"errors"
	"fmt"

	"trading-system/internal/hft/engine"
)

// DefaultPaperVenueName is used when NewPaperVenue receives an empty name.
const DefaultPaperVenueName = "paper"

// ErrCancelNotSupported is returned by the internal paper venue on cancel.
var ErrCancelNotSupported = errors.New("execution: cancel not supported by paper venue")

// ErrNilMatcher is returned by PaperVenue.Submit when no matcher was configured.
var ErrNilMatcher = errors.New("execution: paper venue requires a matcher")

// PaperVenue executes orders against the internal matcher.
// It holds no mutable state, so it is as concurrency-safe as the matcher it wraps.
type PaperVenue struct {
	name    string
	matcher Matcher
}

// NewPaperVenue builds a paper venue. An empty name falls back to "paper".
// A nil matcher is accepted here so construction cannot panic; Submit reports
// ErrNilMatcher instead.
func NewPaperVenue(name string, matcher Matcher) *PaperVenue {
	if name == "" {
		name = DefaultPaperVenueName
	}
	return &PaperVenue{name: name, matcher: matcher}
}

// Name returns the configured venue name.
func (p *PaperVenue) Name() string {
	return p.name
}

// Submit forwards the order to the internal matcher and returns its detailed
// result unchanged, including trades and the final order status.
func (p *PaperVenue) Submit(ctx context.Context, order engine.Order) (engine.SubmitResult, error) {
	if p.matcher == nil {
		return engine.SubmitResult{}, ErrNilMatcher
	}

	result, err := p.matcher.SubmitOrderDetailed(ctx, order)
	if err != nil {
		return engine.SubmitResult{},
			fmt.Errorf("execution: paper venue %q submit order %d: %w", p.name, order.ID, err)
	}

	return result, nil
}

// Cancel is not supported by the internal matching engine.
func (p *PaperVenue) Cancel(_ context.Context, req CancelRequest) error {
	return fmt.Errorf("execution: paper venue %q cancel order %d: %w", p.name, req.OrderID, ErrCancelNotSupported)
}

// Compile-time check that PaperVenue satisfies Venue.
var _ Venue = (*PaperVenue)(nil)
