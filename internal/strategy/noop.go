package strategy

// Noop is a baseline strategy that never trades. It is useful for validating a
// data source or a report pipeline without strategy noise.
type Noop struct{}

// NewNoop builds the no-op strategy.
func NewNoop() *Noop { return &Noop{} }

func (n *Noop) Name() string { return "noop" }

func (n *Noop) Init(Context, map[string]string) error { return nil }

func (n *Noop) OnBook(Context, BookView) error { return nil }

func (n *Noop) OnTrade(Context, TradeView) error { return nil }

func (n *Noop) OnTimer(Context, int64) error { return nil }

func (n *Noop) Stop(Context) error { return nil }

var _ Strategy = (*Noop)(nil)
