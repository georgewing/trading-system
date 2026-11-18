package domain

import "testing"

// Three-segment kline topics are the only topic shape that carries an interval.
// These cases pin both the accepted form and the ambiguous one that must stay
// rejected.
func TestKlineTopicValidation(t *testing.T) {
	f := NewMemFanout(4)

	valid := []string{
		"kline:BTCUSDT:1m",
		"kline:BTCUSDT:1h",
		"depth:BTCUSDT",
		"trade:*",
		"tick:BTCUSDT",
	}
	for _, topic := range valid {
		if _, cancel, err := f.Subscribe([]string{topic}); err != nil {
			t.Errorf("Subscribe(%q) error = %v, want nil", topic, err)
		} else {
			cancel()
		}
	}

	invalid := []string{
		"kline:BTCUSDT",      // interval is required
		"kline:BTCUSDT:",     // empty interval
		"kline::1m",          // empty symbol
		"kline:BTCUSDT:1m:x", // too many segments
		"depth:BTCUSDT:1m",   // interval only applies to kline
		"unknown:BTCUSDT",
	}
	for _, topic := range invalid {
		if _, cancel, err := f.Subscribe([]string{topic}); err == nil {
			t.Errorf("Subscribe(%q) error = nil, want error", topic)
			if cancel != nil {
				cancel()
			}
		}
	}
}

func TestKlineTopicMatching(t *testing.T) {
	f := NewMemFanout(8)

	ch, cancel, err := f.Subscribe([]string{"kline:*"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer cancel()

	ev := Event{
		Kind:   KindKline,
		Symbol: "BTCUSDT",
		Kline:  &Kline{Interval: "1m", OpenTime: 1},
	}
	f.Publish(ev)

	select {
	case got := <-ch:
		if got.Topic() != "kline:BTCUSDT:1m" {
			t.Fatalf("Topic() = %q", got.Topic())
		}
	default:
		t.Fatal("kline event did not match kline:* subscription")
	}
}

func TestEventTopicShapes(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  string
	}{
		{
			name:  "trade",
			event: Event{Kind: KindTrade, Symbol: "BTCUSDT"},
			want:  "trade:BTCUSDT",
		},
		{
			name:  "depth",
			event: Event{Kind: KindDepth, Symbol: "BTCUSDT"},
			want:  "depth:BTCUSDT",
		},
		{
			name: "kline",
			event: Event{
				Kind:   KindKline,
				Symbol: "BTCUSDT",
				Kline:  &Kline{Interval: "5m"},
			},
			want: "kline:BTCUSDT:5m",
		},
		{
			name:  "kline without payload falls back to kind:symbol",
			event: Event{Kind: KindKline, Symbol: "BTCUSDT"},
			want:  "kline:BTCUSDT",
		},
	}

	for _, tc := range cases {
		if got := tc.event.Topic(); got != tc.want {
			t.Errorf("%s: Topic() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
