package main

import (
	"context"
	"fmt"
	"log"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/store"

	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()

	cfg := config.Load()

	db, err := store.NewMySQL(cfg)
	if err != nil {
		log.Fatalf("init mysql failed: %v", err)
	}

	if err := store.SeedAdminUser(db); err != nil {
		log.Fatalf("seed admin failed: %v", err)
	}

	rdb, err := store.NewRedis(context.Background(), cfg)
	if err != nil {
		log.Fatalf("init redis failed: %v", err)
	}

	container := app.NewContainer(cfg, db, rdb)
	r := container.NewRouter()

	addr := fmt.Sprintf(":%s", cfg.AppPort)
	log.Printf("server running on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("run server failed: %v", err)
	}
}
