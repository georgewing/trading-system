package app

import (
	"context"

	"trading-system/internal/execution"
	"trading-system/internal/hft/engine"
)

// ackOnlyVenue adapts an asynchronous external venue to the synchronous Venue
// contract used by EventBus. REST submission is treated as an acknowledgement;
// fills are authoritative only from the venue user-data stream.
//
// It also clears ClientOrderID before submitting so the venue uses the numeric
// engine order ID as newClientOrderId. That lets user-stream reports map back to
// the engine order without an external order-ID index.
type ackOnlyVenue struct {
	execution.Venue
	quantityScale int64
}

func (v *ackOnlyVenue) Submit(ctx context.Context, order engine.Order) (engine.SubmitResult, error) {
	order.ClientOrderID = ""
	if _, err := v.Venue.Submit(ctx, order); err != nil {
		return engine.SubmitResult{}, err
	}

	qty, err := engine.ParseQuantity(order.Quantity, v.quantityScale)
	if err != nil {
		return engine.SubmitResult{}, err
	}
	return engine.SubmitResult{
		OpenLeaves:  qty,
		FinalStatus: engine.OrderStatusNew,
	}, nil
}

var _ execution.Venue = (*ackOnlyVenue)(nil)
