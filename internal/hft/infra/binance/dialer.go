package binance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Default connection parameters for the public spot stream.
const (
	defaultBaseURL          = "wss://stream.binance.com:9443"
	defaultDepthLevels      = 20
	defaultDepthSpeed       = "100ms"
	defaultHandshakeTimeout = 15 * time.Second
	defaultReadLimit        = 8 << 20
)

// StreamConfig selects which streams a connection subscribes to.
//
// Only public market data is used: no API key, no account and no trading
// permission is involved.
type StreamConfig struct {
	BaseURL     string
	DepthLevels int    // 5, 10 or 20; 0 uses the default
	DepthSpeed  string // "100ms" or "1000ms"; empty uses the default
	// KlineInterval may be empty to disable the kline stream entirely. It is
	// never defaulted here, so an empty value cannot silently enable a stream.
	KlineInterval    string
	HandshakeTimeout time.Duration
	ReadLimit        int64
}

// NewWebsocketDialer validates cfg and returns a Dialer that opens one
// combined-stream connection per call.
func NewWebsocketDialer(cfg StreamConfig) (Dialer, error) {
	cfg = cfg.withDefaults()

	switch cfg.DepthLevels {
	case 5, 10, 20:
	default:
		return nil, fmt.Errorf("binance: unsupported depth levels %d (want 5, 10 or 20)", cfg.DepthLevels)
	}
	if cfg.DepthSpeed != "100ms" && cfg.DepthSpeed != "1000ms" {
		return nil, fmt.Errorf("binance: unsupported depth speed %q", cfg.DepthSpeed)
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("binance: base url is required")
	}

	return func(ctx context.Context, symbols []string) (Stream, error) {
		url, err := cfg.URL(symbols)
		if err != nil {
			return nil, err
		}

		dialCtx, cancel := context.WithTimeout(ctx, cfg.HandshakeTimeout)
		defer cancel()

		conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return nil, fmt.Errorf("binance: dial %s: %w", url, err)
		}
		conn.SetReadLimit(cfg.ReadLimit)

		return &wsStream{conn: conn}, nil
	}, nil
}

// URL builds the combined-stream URL for the given symbols.
func (c StreamConfig) URL(symbols []string) (string, error) {
	streams, err := c.streams(symbols)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(c.BaseURL, "/") +
		"/stream?streams=" + strings.Join(streams, "/"), nil
}

// streams expands symbols into stream names:
//
//	btcusdt@depth20@100ms   top-N book snapshots
//	btcusdt@trade           trade prints
//	btcusdt@kline_1m        candles (optional)
func (c StreamConfig) streams(symbols []string) ([]string, error) {
	if len(symbols) == 0 {
		return nil, errors.New("binance: no symbols")
	}

	out := make([]string, 0, len(symbols)*3)
	for _, raw := range symbols {
		symbol := strings.ToLower(strings.TrimSpace(raw))
		if symbol == "" {
			return nil, errors.New("binance: empty symbol")
		}

		out = append(out,
			fmt.Sprintf("%s@depth%d@%s", symbol, c.DepthLevels, c.DepthSpeed),
			symbol+"@trade",
		)
		if c.KlineInterval != "" {
			out = append(out, fmt.Sprintf("%s@kline_%s", symbol, c.KlineInterval))
		}
	}
	return out, nil
}

func (c StreamConfig) withDefaults() StreamConfig {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.DepthLevels == 0 {
		c.DepthLevels = defaultDepthLevels
	}
	if c.DepthSpeed == "" {
		c.DepthSpeed = defaultDepthSpeed
	}
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = defaultHandshakeTimeout
	}
	if c.ReadLimit <= 0 {
		c.ReadLimit = defaultReadLimit
	}
	return c
}

// wsStream adapts a coder/websocket connection to the Stream port.
type wsStream struct {
	conn *websocket.Conn
}

func (s *wsStream) Read(ctx context.Context) ([]byte, error) {
	_, data, err := s.conn.Read(ctx)
	return data, err
}

func (s *wsStream) Close() error {
	// coder/websocket bounds the closing handshake internally; there is no
	// context parameter to thread here.
	return s.conn.Close(websocket.StatusNormalClosure, "")
}
