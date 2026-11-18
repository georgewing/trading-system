// Package routing selects an execution venue for an order symbol.
// Routers are immutable after construction and safe for concurrent reads.
package routing

import (
	"errors"
	"fmt"

	"trading-system/internal/execution"
)

var (
	// ErrUnknownSymbol reports a symbol with no explicit mapping and no usable default.
	ErrUnknownSymbol = errors.New("routing: unknown symbol")
	// ErrUnknownVenue reports a reference to a venue that is not configured.
	ErrUnknownVenue = errors.New("routing: unknown venue")
)

// Router resolves symbols to venues using an exact symbol mapping with a
// default venue fallback. The zero value is unusable; call NewRouter.
type Router struct {
	defaultVenue string
	venues       map[string]execution.Venue
	symbolVenues map[string]string
}

// NewRouter validates the venue set and builds an immutable router.
// defaultVenue must exist in venues, every venue name must be non-empty and
// map to a non-nil venue, and every symbol mapping must point to a known venue.
func NewRouter(defaultVenue string, venues map[string]execution.Venue, symbolVenues map[string]string) (*Router, error) {
	if defaultVenue == "" {
		return nil, errors.New("routing: default venue is empty")
	}
	if len(venues) == 0 {
		return nil, errors.New("routing: at least one venue is required")
	}

	for name, venue := range venues {
		if name == "" {
			return nil, errors.New("routing: venue name is empty")
		}
		if venue == nil {
			return nil, fmt.Errorf("routing: venue %q is nil", name)
		}
	}

	if _, ok := venues[defaultVenue]; !ok {
		return nil, fmt.Errorf("routing: default venue %q: %w", defaultVenue, ErrUnknownVenue)
	}

	for symbol, name := range symbolVenues {
		if name == "" {
			return nil, fmt.Errorf("routing: symbol %q maps to an empty venue", symbol)
		}
		if _, ok := venues[name]; !ok {
			return nil, fmt.Errorf("routing: symbol %q maps to venue %q: %w", symbol, name, ErrUnknownVenue)
		}
	}

	// Copy caller maps so later mutations cannot change routing behavior.
	venueCopy := make(map[string]execution.Venue, len(venues))
	for name, venue := range venues {
		venueCopy[name] = venue
	}
	symbolCopy := make(map[string]string, len(symbolVenues))
	for symbol, name := range symbolVenues {
		symbolCopy[symbol] = name
	}

	return &Router{
		defaultVenue: defaultVenue,
		venues:       venueCopy,
		symbolVenues: symbolCopy,
	}, nil
}

// Route returns the venue for symbol: exact symbol mapping first, then the
// default venue.
func (r *Router) Route(symbol string) (execution.Venue, error) {
	if name, ok := r.symbolVenues[symbol]; ok {
		venue, ok := r.venues[name]
		if !ok {
			return nil, fmt.Errorf("routing: symbol %q maps to venue %q: %w", symbol, name, ErrUnknownVenue)
		}
		return venue, nil
	}

	if r.defaultVenue == "" {
		return nil, fmt.Errorf("routing: symbol %q: %w", symbol, ErrUnknownSymbol)
	}

	venue, ok := r.venues[r.defaultVenue]
	if !ok {
		return nil, fmt.Errorf("routing: symbol %q default venue %q: %w", symbol, r.defaultVenue, ErrUnknownVenue)
	}
	return venue, nil
}

// Default returns the configured default venue, or nil for a zero-value Router.
func (r *Router) Default() execution.Venue {
	return r.venues[r.defaultVenue]
}
