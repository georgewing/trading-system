package binanceexec

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// VenueName is the name returned by Venue.Name and stamped on every
	// execution report produced by this package.
	VenueName = "binance"

	// DefaultBaseURL is the Binance Spot testnet REST root. Live trading must
	// be opted into explicitly by setting Config.BaseURL.
	DefaultBaseURL = "https://testnet.binance.vision"
	// DefaultWSURL is the Binance Spot testnet stream root. Live trading must
	// be opted into explicitly by setting Config.WSURL.
	DefaultWSURL = "wss://stream.testnet.binance.vision"

	// DefaultRecvWindow is used when Config.RecvWindow is zero. It matches the
	// Binance default.
	DefaultRecvWindow = 5 * time.Second
	// MaxRecvWindow is Binance's documented upper bound for recvWindow.
	MaxRecvWindow = 60 * time.Second

	// DefaultReconnectMin and DefaultReconnectMax bound user stream reconnect
	// backoff when the corresponding Config fields are zero.
	DefaultReconnectMin = 500 * time.Millisecond
	DefaultReconnectMax = 10 * time.Second

	defaultHTTPTimeout = 10 * time.Second

	// clockSyncTimeout bounds the lazy clock offset probe so that it can never
	// dominate a signed request.
	clockSyncTimeout = 2 * time.Second
	// clockSyncRetry throttles retries after a failed probe.
	clockSyncRetry = 30 * time.Second

	// listenKeyKeepaliveInterval is well below the 60 minute validity Binance
	// grants a listen key, leaving room for a single retry.
	listenKeyKeepaliveInterval = 30 * time.Minute
	listenKeyCloseTimeout      = 3 * time.Second

	wsHandshakeTimeout  = 10 * time.Second
	maxResponseBytes    = 1 << 20
	userStreamReadLimit = 1 << 20
)

// clientOrderIDPattern is the Binance newClientOrderId charset and length.
var clientOrderIDPattern = regexp.MustCompile(`^[\.A-Za-z0-9_:\-/]{1,36}$`)

// Config configures a Binance Spot execution venue.
//
// Credentials are supplied by the application (config file or environment);
// this package never reads the environment and never logs them.
type Config struct {
	// BaseURL is the REST root, for example https://testnet.binance.vision.
	// Empty means DefaultBaseURL; live endpoints must be set explicitly.
	BaseURL string
	// WSURL is the stream root used for the user data stream, for example
	// wss://stream.testnet.binance.vision. The listen key is appended as
	// /ws/<listenKey>. Empty means DefaultWSURL.
	WSURL string
	// APIKey is the API key sent in X-MBX-APIKEY. Required.
	APIKey string
	// APISecret is the HMAC-SHA256 signing secret. Required; never logged.
	APISecret string
	// AccountID is stamped on execution reports derived from the user stream.
	AccountID uint64
	// RecvWindow is the Binance recvWindow. Zero means DefaultRecvWindow,
	// the valid range is 1ms..MaxRecvWindow.
	RecvWindow time.Duration
	// PriceScale is the multiplier that turns price ticks into decimal, for
	// example 100000000 for eight decimals. Must be a positive power of ten.
	PriceScale int64
	// QuantityScale is the multiplier that turns quantity lots into decimal.
	// Must be a positive power of ten.
	QuantityScale int64
	// HTTPClient is used for REST and the websocket handshake. Empty means a
	// client with a 10s timeout. Optional.
	HTTPClient *http.Client
	// ReconnectMin and ReconnectMax bound user stream reconnect backoff.
	// Zero values fall back to DefaultReconnectMin/DefaultReconnectMax.
	ReconnectMin time.Duration
	ReconnectMax time.Duration
}

// normalizeConfig applies defaults and validates every field that later code
// relies on. It performs no network access.
func normalizeConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return cfg, fmt.Errorf("%w: APIKey is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(cfg.APISecret) == "" {
		return cfg, fmt.Errorf("%w: APISecret is required", ErrInvalidConfig)
	}
	if !isPowerOfTen(cfg.PriceScale) {
		return cfg, fmt.Errorf("%w: PriceScale %d must be a positive power of ten", ErrInvalidConfig, cfg.PriceScale)
	}
	if !isPowerOfTen(cfg.QuantityScale) {
		return cfg, fmt.Errorf("%w: QuantityScale %d must be a positive power of ten", ErrInvalidConfig, cfg.QuantityScale)
	}

	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return cfg, err
	}

	if cfg.WSURL == "" {
		cfg.WSURL = DefaultWSURL
	}
	cfg.WSURL = strings.TrimRight(strings.TrimSpace(cfg.WSURL), "/")
	if err := validateWSURL(cfg.WSURL); err != nil {
		return cfg, err
	}

	if cfg.RecvWindow == 0 {
		cfg.RecvWindow = DefaultRecvWindow
	}
	if cfg.RecvWindow < time.Millisecond || cfg.RecvWindow > MaxRecvWindow {
		return cfg, fmt.Errorf("%w: RecvWindow %s must be between 1ms and %s",
			ErrInvalidConfig, cfg.RecvWindow, MaxRecvWindow)
	}

	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}

	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = DefaultReconnectMin
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = DefaultReconnectMax
	}
	if cfg.ReconnectMax < cfg.ReconnectMin {
		return cfg, fmt.Errorf("%w: ReconnectMax %s is smaller than ReconnectMin %s",
			ErrInvalidConfig, cfg.ReconnectMax, cfg.ReconnectMin)
	}

	return cfg, nil
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: BaseURL: %v", ErrInvalidConfig, err)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: BaseURL has no host", ErrInvalidConfig)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: BaseURL scheme must be http or https", ErrInvalidConfig)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: BaseURL must not carry a query or fragment", ErrInvalidConfig)
	}
	return nil
}

func validateWSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: WSURL: %v", ErrInvalidConfig, err)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: WSURL has no host", ErrInvalidConfig)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("%w: WSURL scheme must be ws or wss", ErrInvalidConfig)
	}
	return nil
}

// isPowerOfTen reports whether v is 1, 10, 100, ...
func isPowerOfTen(v int64) bool {
	if v <= 0 {
		return false
	}
	for v > 1 && v%10 == 0 {
		v /= 10
	}
	return v == 1
}

// decimalPlaces returns the number of decimal digits for a validated scale.
func decimalPlaces(scale int64) int {
	n := 0
	for scale > 1 {
		scale /= 10
		n++
	}
	return n
}

// formatUnits renders a minimal-unit integer as a decimal string. The result
// carries no trailing fractional zeros, for example (12340, 100) -> "123.4"
// and (5, 100) -> "0.05". The scale must be a positive power of ten.
func formatUnits(value, scale int64) string {
	sign := ""
	u := uint64(value)
	if value < 0 {
		sign = "-"
		// Negate without overflowing on math.MinInt64.
		u = uint64(-(value + 1)) + 1
	}

	s := uint64(scale)
	whole := u / s
	frac := u % s
	if frac == 0 {
		return sign + strconv.FormatUint(whole, 10)
	}

	digits := strconv.FormatUint(frac, 10)
	if pad := decimalPlaces(scale) - len(digits); pad > 0 {
		digits = strings.Repeat("0", pad) + digits
	}
	digits = strings.TrimRight(digits, "0")

	return sign + strconv.FormatUint(whole, 10) + "." + digits
}

// validClientOrderID reports whether id matches the Binance newClientOrderId
// charset and length. Rejecting locally produces a clearer error than a venue
// rejection and avoids a pointless round trip.
func validClientOrderID(id string) bool {
	return clientOrderIDPattern.MatchString(id)
}
