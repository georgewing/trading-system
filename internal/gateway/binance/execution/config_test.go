package binanceexec

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		APIKey:        "key",
		APISecret:     "secret",
		PriceScale:    100_000_000,
		QuantityScale: 100_000_000,
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	venue, err := New(validConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if got := venue.Name(); got != VenueName {
		t.Fatalf("Name() = %q, want %q", got, VenueName)
	}
	if venue.cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", venue.cfg.BaseURL, DefaultBaseURL)
	}
	if venue.cfg.WSURL != DefaultWSURL {
		t.Fatalf("WSURL = %q, want %q", venue.cfg.WSURL, DefaultWSURL)
	}
	if venue.cfg.RecvWindow != DefaultRecvWindow {
		t.Fatalf("RecvWindow = %s, want %s", venue.cfg.RecvWindow, DefaultRecvWindow)
	}
	if venue.cfg.ReconnectMin != DefaultReconnectMin || venue.cfg.ReconnectMax != DefaultReconnectMax {
		t.Fatalf("reconnect = %s..%s, want %s..%s",
			venue.cfg.ReconnectMin, venue.cfg.ReconnectMax, DefaultReconnectMin, DefaultReconnectMax)
	}
	if venue.cfg.HTTPClient == nil {
		t.Fatal("HTTPClient = nil, want a default client")
	}
}

func TestNewKeepsExplicitEndpoints(t *testing.T) {
	cfg := validConfig()
	cfg.BaseURL = "http://127.0.0.1:1234/"
	cfg.WSURL = "ws://127.0.0.1:1234/"

	venue, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if venue.cfg.BaseURL != "http://127.0.0.1:1234" {
		t.Fatalf("BaseURL = %q, want trailing slash trimmed", venue.cfg.BaseURL)
	}
	if venue.cfg.WSURL != "ws://127.0.0.1:1234" {
		t.Fatalf("WSURL = %q, want trailing slash trimmed", venue.cfg.WSURL)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty api key", mutate: func(c *Config) { c.APIKey = "" }},
		{name: "blank api key", mutate: func(c *Config) { c.APIKey = "   " }},
		{name: "empty api secret", mutate: func(c *Config) { c.APISecret = "" }},
		{name: "zero price scale", mutate: func(c *Config) { c.PriceScale = 0 }},
		{name: "negative price scale", mutate: func(c *Config) { c.PriceScale = -100 }},
		{name: "non decimal power price scale", mutate: func(c *Config) { c.PriceScale = 30 }},
		{name: "zero quantity scale", mutate: func(c *Config) { c.QuantityScale = 0 }},
		{name: "non decimal power quantity scale", mutate: func(c *Config) { c.QuantityScale = 3 }},
		{name: "base url wrong scheme", mutate: func(c *Config) { c.BaseURL = "ftp://example.com" }},
		{name: "base url without host", mutate: func(c *Config) { c.BaseURL = "https://" }},
		{name: "base url with query", mutate: func(c *Config) { c.BaseURL = "https://example.com?x=1" }},
		{name: "ws url wrong scheme", mutate: func(c *Config) { c.WSURL = "https://example.com" }},
		{name: "ws url without host", mutate: func(c *Config) { c.WSURL = "wss://" }},
		{name: "recv window too small", mutate: func(c *Config) { c.RecvWindow = time.Microsecond }},
		{name: "recv window too large", mutate: func(c *Config) { c.RecvWindow = MaxRecvWindow + time.Millisecond }},
		{name: "reconnect max below min", mutate: func(c *Config) {
			c.ReconnectMin = time.Second
			c.ReconnectMax = 500 * time.Millisecond
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			_, err := New(cfg)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
			}
			if strings.Contains(err.Error(), cfg.APISecret) && cfg.APISecret != "" {
				t.Fatalf("error leaked API secret: %v", err)
			}
			if strings.Contains(err.Error(), cfg.APIKey) && cfg.APIKey != "" {
				t.Fatalf("error leaked API key: %v", err)
			}
		})
	}
}

func TestFormatUnits(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		scale int64
		want  string
	}{
		{name: "zero", value: 0, scale: 100_000_000, want: "0"},
		{name: "whole", value: 100, scale: 1, want: "100"},
		{name: "fraction", value: 150_000_000, scale: 100_000_000, want: "1.5"},
		{name: "leading zeros", value: 5, scale: 100, want: "0.05"},
		{name: "trim trailing zero", value: 12_340, scale: 100, want: "123.4"},
		{name: "one decimal", value: 1, scale: 10, want: "0.1"},
		{name: "negative", value: -5, scale: 100, want: "-0.05"},
		{name: "min int64", value: math.MinInt64, scale: 100_000_000, want: "-92233720368.54775808"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUnits(tt.value, tt.scale); got != tt.want {
				t.Fatalf("formatUnits(%d, %d) = %q, want %q", tt.value, tt.scale, got, tt.want)
			}
		})
	}
}

func TestValidClientOrderID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "numeric", id: "1001", want: true},
		{name: "allowed punctuation", id: "a.b:c/d-e_f", want: true},
		{name: "36 chars", id: strings.Repeat("a", 36), want: true},
		{name: "empty", id: "", want: false},
		{name: "37 chars", id: strings.Repeat("a", 37), want: false},
		{name: "space", id: "client 7", want: false},
		{name: "non ascii", id: "\u8ba2\u53551", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validClientOrderID(tt.id); got != tt.want {
				t.Fatalf("validClientOrderID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
