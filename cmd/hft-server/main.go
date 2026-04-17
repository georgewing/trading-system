package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"trading-system/internal/app"
)

func main() {
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
	if err := app.Run(ctx); err != nil {
		log.Fatalf("app run error: %v", err)
	}
	log.Println("app started")
}
