// Command feedcheck runs the Binance market feed in isolation so transport and
// decoding problems can be diagnosed without the rest of the server.
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

	"trading-system/internal/gateway/binance"
	"trading-system/internal/market"
)

func main() {
	cfg := binance.DefaultConfig()
	store := market.NewStore(market.DefaultStoreConfig())

	client, err := binance.NewClient(cfg, store, nil)
	if err != nil {
		log.Fatalf("build client: %v", err)
	}
	log.Printf("stream: %s", client.URL())

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	events, stop := store.Subscribe(4096)
	defer stop()

	go func() {
		if err := client.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("feed stopped: %v", err)
		}
	}()

	counts := map[string]int{}
	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()

	for {
		select {
		case ev := <-events:
			counts[ev.Type]++
		case <-tick.C:
			fmt.Printf("depth=%d trade=%d kline=%d dropped=%d\n",
				counts[market.EventDepth], counts[market.EventTrade],
				counts[market.EventKline], store.Dropped())
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
