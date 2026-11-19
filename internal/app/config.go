package app

import (
	"os"

	"go.yaml.in/yaml/v2"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Matcher   MatcherConfig   `yaml:"matcher"`
	Market    MarketConfig    `yaml:"market"`
	Execution ExecutionConfig `yaml:"execution"`
	Pipeline  PipelineConfig  `yaml:"pipeline"`
	Routing   RoutingConfig   `yaml:"routing"`
	Strategy  StrategyConfig  `yaml:"strategy"`
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

// StorageConfig holds datastore connection strings and toggles. Writers are
// disabled unless the corresponding Enabled flag is true, so the example config
// can be used without local databases.
type StorageConfig struct {
	PostgresEnabled          bool   `yaml:"postgres_enabled"`
	PostgresDSN              string `yaml:"postgres_dsn"`
	ClickHouseEnabled        bool   `yaml:"clickhouse_enabled"`
	ClickHouseURL            string `yaml:"clickhouse_url"`
	ClickHouseDatabase       string `yaml:"clickhouse_database"`
	ClickHouseTable          string `yaml:"clickhouse_table"`
	ClickHouseTimeoutSeconds int    `yaml:"clickhouse_timeout_seconds"`
	PositionSnapshotSeconds  int    `yaml:"position_snapshot_seconds"`
}
type WebSocketConfig struct {
	BufferSize int `yaml:"buffer_size"`
}

type PipelineConfig struct {
	InboundCapacity  uint64 `yaml:"inbound_capacity"`
	OutboundCapacity uint64 `yaml:"outbound_capacity"`
}

// RoutingConfig configures order-to-venue routing. "paper" is always
// available; "binance" exists when execution.binance.enabled is true. An empty
// default_venue falls back to "paper".
type RoutingConfig struct {
	DefaultVenue string            `yaml:"default_venue"`
	SymbolVenues map[string]string `yaml:"symbol_venues"`
}

// ExecutionConfig configures external execution venues. Only Binance Spot is
// supported today; credentials are supplied through environment variables.
type ExecutionConfig struct {
	Binance BinanceExecutionConfig `yaml:"binance"`
}

type BinanceExecutionConfig struct {
	Enabled             bool   `yaml:"enabled"`
	BaseURL             string `yaml:"base_url"`
	WSURL               string `yaml:"ws_url"`
	AccountID           uint64 `yaml:"account_id"`
	RecvWindowSeconds   int    `yaml:"recv_window_seconds"`
	ReconnectMinSeconds int    `yaml:"reconnect_min_seconds"`
	ReconnectMaxSeconds int    `yaml:"reconnect_max_seconds"`
}

// RiskConfig configures the optional risk stages. Zero/empty values disable the
// corresponding check; Enabled gates the whole stage.
type RiskConfig struct {
	Enabled            bool     `yaml:"enabled"`
	SymbolWhitelist    []string `yaml:"symbol_whitelist"`
	MaxOrderQty        int64    `yaml:"max_order_qty"`
	MaxNotional        int64    `yaml:"max_notional"`
	PriceBandBps       int64    `yaml:"price_band_bps"`
	MaxOrdersPerWindow int      `yaml:"max_orders_per_window"`
	WindowSeconds      int      `yaml:"window_seconds"`
	MaxPosition        int64    `yaml:"max_position"`

	Intraday IntradayRiskConfig `yaml:"intraday"`
}

// IntradayRiskConfig configures post-trade monitoring and strategy breakers.
// Zero values disable the corresponding check.
type IntradayRiskConfig struct {
	MaxCancelRatioBps   int64 `yaml:"max_cancel_ratio_bps"`
	CancelWindowSeconds int   `yaml:"cancel_window_seconds"`
	MinCancelSamples    int   `yaml:"min_cancel_samples"`
	MaxOpenOrders       int   `yaml:"max_open_orders"`
	MaxDailyLoss        int64 `yaml:"max_daily_loss"`
	MaxDrawdownBps      int64 `yaml:"max_drawdown_bps"`
	CooldownSeconds     int   `yaml:"cooldown_seconds"`
	ResumeAuto          bool  `yaml:"resume_auto"`
}

// StrategyConfig configures the optional live strategy runtime. Only
// single-symbol strategies are supported live: market_maker and noop.
type StrategyConfig struct {
	Enabled bool   `yaml:"enabled"`
	Name    string `yaml:"name"`
	// AccountID is required: strategy orders are risk-checked and attributed.
	AccountID uint64 `yaml:"account_id"`
	// Symbol defaults to matcher.symbol; another symbol is rejected because the
	// gateway still serves one book.
	Symbol string `yaml:"symbol"`

	TimerSeconds        int `yaml:"timer_seconds"`
	SubmitTimeoutMillis int `yaml:"submit_timeout_millis"`
	MaxOrderRate        int `yaml:"max_order_rate"`
	MaxOpenOrders       int `yaml:"max_open_orders"`
	MaxPanics           int `yaml:"max_panics"`

	Params map[string]string `yaml:"params"`
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
