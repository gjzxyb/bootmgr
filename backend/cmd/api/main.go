package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/database"
	"baremetal-platform/backend/internal/handlers"
	"baremetal-platform/backend/internal/services"
)

func main() {
	cfg := config.Load()
	validation := config.Validate(cfg)
	for _, issue := range validation.Warnings {
		log.Printf("configuration warning [%s]: %s", issue.Key, issue.Message)
	}
	for _, issue := range validation.Errors {
		log.Printf("configuration error [%s]: %s", issue.Key, issue.Message)
	}
	if validation.HasErrors() {
		log.Fatal("configuration validation failed")
	}
	if err := config.CheckImageStorage(cfg.ImageStorageDir); err != nil {
		log.Fatalf("image storage check failed: %v", err)
	}
	db, err := database.ConnectWithRetry(cfg, log.Printf)
	if err != nil {
		log.Fatalf("database initialization failed: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := services.NewPXEService(db, cfg, log.Printf).Start(ctx); err != nil {
		log.Fatalf("boot service initialization failed: %v", err)
	}
	r := handlers.NewRouter(db, cfg)
	log.Printf("baremetal platform API listening on %s", cfg.HTTPAddr)
	if err := r.Run(cfg.HTTPAddr); err != nil {
		log.Fatal(err)
	}
}
