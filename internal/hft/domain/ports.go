package domain

import "context"

// Emitter receives normalized events from a Source. It is called from the
// source read loop and must not block: producers drop or buffer on their own
// side instead of stalling the connection.
type Emitter func(Event)

// Source streams normalized market data for a set of symbols.
//
// Implementations own their connection lifecycle, including dial, heartbeat and
// reconnect. Run blocks until ctx is cancelled and returns nil on a graceful
// stop; a non-nil error means the source could not even start.
type Source interface {
	Name() string
	Run(ctx context.Context, symbols []string, emit Emitter) error
}

// Sink persists batches of normalized events. WriteBatch owns retry and
// backpressure decisions; Close must flush whatever is still buffered.
type Sink interface {
	Name() string
	WriteBatch(ctx context.Context, batch []Event) error
	Close(ctx context.Context) error
}

// Fanout distributes events to topic subscribers. Publish must never block the
// caller: slow subscribers lose events (and are counted) instead of stalling
// the ingest path.
type Fanout interface {
	Publish(ev Event)
	Subscribe(topics []string) (<-chan Event, func(), error)
}
