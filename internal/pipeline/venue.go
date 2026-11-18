package pipeline

import (
	"errors"

	"trading-system/internal/execution"
)

// VenueResolver selects the execution venue for an order symbol. It is
// implemented by *routing.Router; tests may provide a fake.
type VenueResolver interface {
	Route(symbol string) (execution.Venue, error)
}

// errNilVenue guards against a resolver that reports success without a venue,
// which would otherwise panic when the bus calls Submit.
var errNilVenue = errors.New("pipeline: venue resolver returned nil venue")

// routingRejectPrefix marks ExecReport reasons produced by the routing stage so
// clients can tell them apart from risk codes and venue submission errors.
const routingRejectPrefix = "routing rejected: "

// SetVenueResolver installs the optional routing stage. When resolver is nil
// orders go straight to the matcher, which is the default behavior.
//
// Call it before Start; the resolver is read on the single processing goroutine.
func (b *EventBus) SetVenueResolver(resolver VenueResolver) {
	b.venueResolver = resolver
}

// routingRejectReason renders a routing failure as the ExecReport reason while
// keeping the underlying error text intact.
func routingRejectReason(err error) string {
	return routingRejectPrefix + err.Error()
}
