// Command feedcheck runs the Binance market-data source in isolation so
// transport and decoding problems can be diagnosed without the rest of the
// server.
//
//	go run ./scripts/dev/feedcheck
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"trading-system/internal/gateway/binance/market"
	"trading-system/internal/hft/domain"
	"trading-system/internal/hft/infra/memstore"
)

func main() {
	const symbol = "BTCUSDT"
	const scale = 100_000_000 // eight decimals

	dialer, err := binance.NewWebsocketDialer(binance.StreamConfig{
		DepthLevels:   20,
		DepthSpeed:    "100ms",
		KlineInterval: "1m",
	})
	if err != nil {
		log.Fatalf("dialer: %v", err)
	}

	src, err := binance.NewSource(binance.SourceConfig{
		Decoder: binance.Decoder{PriceScale: scale, QtyScale: scale, Symbol: symbol},
		Dial:    dialer,
	})
	if err != nil {
		log.Fatalf("source: %v", err)
	}

	cache := memstore.New(memstore.Config{})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	counts := map[domain.Kind]int{}
	emit := func(ev domain.Event) {
		cache.Apply(ev)
		counts[ev.Kind]++
	}

	go func() {
		_ = src.Run(ctx, []string{symbol}, emit)
	}()

	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			fmt.Printf("depth=%d trade=%d kline=%d reconnects=%d decode_errors=%d\n",
				counts[domain.KindDepth], counts[domain.KindTrade], counts[domain.KindKline],
				src.Reconnects(), src.DecodeErrors())
		case <-deadline:
			if len(counts) == 0 {
				log.Printf("no events after 20s")
				os.Exit(1)
			}
			return
		case <-ctx.Done():
			return
		}
	}
}
