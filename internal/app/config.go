package app

import (
	"os"

	"go.yaml.in/yaml/v2"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Matcher   MatcherConfig   `yaml:"matcher"`
	Market    MarketConfig    `yaml:"market"`
	Pipeline  PipelineConfig  `yaml:"pipeline"`
	Risk      RiskConfig      `yaml:"risk"`
	Storage   StorageConfig   `yaml:"storage"`
	WebSocket WebSocketConfig `yaml:"websocket"`
}

type ServerConfig struct {
	APIAddr     string `yaml:"api_addr"`
	MetricsAddr string `yaml:"metrics_addr"`
}

type MatcherConfig struct {
	Symbol string `yaml:"symbol"`
	// PricePrecision and QuantityPrecision are decimal places, not multipliers.
	PricePrecision    int `yaml:"price_precision"`
	QuantityPrecision int `yaml:"quantity_precision"`
}

// MarketConfig configures the external real-time market-data feed that backs
// the trading terminal. The feed is read-only and never touches matching.
type MarketConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Provider string `yaml:"provider"` // only "binance" is supported today
	WSURL    string `yaml:"ws_url"`   // e.g. wss://stream.binance.com:9443
	Symbol   string `yaml:"symbol"`   // empty falls back to matcher.symbol

	DepthLevels   int    `yaml:"depth_levels"`   // 5, 10 or 20
	DepthSpeed    string `yaml:"depth_speed"`    // 100ms or 1000ms
	KlineInterval string `yaml:"kline_interval"` // 1m, 5m, ...

	ReconnectMinSeconds int `yaml:"reconnect_min_seconds"`
	ReconnectMaxSeconds int `yaml:"reconnect_max_seconds"`

	StoreTradeCap int `yaml:"store_trade_cap"`
	StoreKlineCap int `yaml:"store_kline_cap"`
}

// StorageConfig holds datastore connection strings.
type StorageConfig struct {
	PostgresDSN string `yaml:"postgres_dsn"`
}
type WebSocketConfig struct {
	BufferSize int `yaml:"buffer_size"`
}

type PipelineConfig struct {
	InboundCapacity  uint64 `yaml:"inbound_capacity"`
	OutboundCapacity uint64 `yaml:"outbound_capacity"`
}

// RiskConfig configures the optional pre-trade risk stage. Zero/empty values
// disable the corresponding check; Enabled gates the whole stage.
type RiskConfig struct {
	Enabled            bool     `yaml:"enabled"`
	SymbolWhitelist    []string `yaml:"symbol_whitelist"`
	MaxOrderQty        int64    `yaml:"max_order_qty"`
	MaxNotional        int64    `yaml:"max_notional"`
	PriceBandBps       int64    `yaml:"price_band_bps"`
	MaxOrdersPerWindow int      `yaml:"max_orders_per_window"`
	WindowSeconds      int      `yaml:"window_seconds"`
	MaxPosition        int64    `yaml:"max_position"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
