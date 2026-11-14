package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"necipdrive/internal/app"
	"necipdrive/internal/backup"
	"necipdrive/internal/config"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		os.Exit(backup.RunCLI(os.Args[2:]))
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server, cleanup, err := app.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("listening on %s", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
