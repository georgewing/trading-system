package app

import (
	"go.yaml.in/yaml/v2"
	"os"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Matcher   MatcherConfig   `yaml:"matcher"`
	Pipeline  PipelineConfig  `yaml:"pipeline"`
	Storage   StorageConfig   `yaml:"storage"`
	WebSocket WebSocketConfig `yaml:"websocket"`
}

type ServerConfig struct {
	APIAddr     string `yaml:"api_addr"`
	MetricsAddr string `yaml:"metrics_addr"`
}

type MatcherConfig struct {
	Symbol            string `yaml:"symbol"`
	PricePrecision    int    `yaml:"price_precision"`
	QuantityPrecision int    `yaml:"quantity_precision"`
}
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
