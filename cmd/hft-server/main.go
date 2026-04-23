package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"trading-system/internal/app"
)

func main() {
	configPath := flag.String("config", "", "path to yaml config")
	flag.Parse()

	if *configPath == "" {
		*configPath = os.Getenv("APP_CONFIG")
	}
	if *configPath == "" {
		log.Fatal("config path is required: use -config or APP_CONFIG")
	}

	cfg, err := app.Load(*configPath)
	if err != nil {
		log.Fatalf("load config failed (%s): %v", *configPath, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 监听中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutting down...")
		cancel()
	}()

	// 启动应用
	if err := app.Run(ctx, cfg); err != nil {
		log.Fatalf("app run error: %v", err)
	}
	log.Println("app started")
}
