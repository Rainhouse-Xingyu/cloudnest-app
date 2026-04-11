package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/store"

	"github.com/joho/godotenv"
)

var (
	buildVersion = "dev"
	buildCommit  = "none"
	buildTime    = "unknown"
)

func main() {
	godotenv.Load()

	cfg := config.Load()
	if err := applyCLIOverrides(&cfg, os.Args[1:]); err != nil {
		log.Fatalf("invalid startup args: %v", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("prepare data dir failed: %v", err)
	}

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
	log.Printf("server running on %s, data_dir=%s", addr, cfg.DataDir)
	if err := r.Run(addr); err != nil {
		log.Fatalf("run server failed: %v", err)
	}
}

func applyCLIOverrides(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return nil
	}

	if args[0] == "version" {
		printVersion()
		os.Exit(0)
	}

	if isHelpArg(args[0]) {
		printUsage()
		os.Exit(0)
	}

	if args[0] == "start" {
		args = args[1:]
	}

	if len(args) > 2 {
		return errors.New("usage: cloudnest [start] [port] [data_dir]")
	}

	if len(args) >= 1 && args[0] != "" {
		if !isValidPort(args[0]) {
			return fmt.Errorf("invalid port: %s", args[0])
		}
		cfg.AppPort = args[0]
	}

	if len(args) == 2 {
		cfg.DataDir = filepath.Clean(args[1])
	}

	return nil
}

func isHelpArg(s string) bool {
	return s == "-h" || s == "--help" || s == "help"
}

func isValidPort(s string) bool {
	p, err := strconv.Atoi(s)
	if err != nil {
		return false
	}
	return p > 0 && p <= 65535
}

func printUsage() {
	fmt.Println("CloudNest Server")
	fmt.Println("Usage:")
	fmt.Println("  cloudnest")
	fmt.Println("  cloudnest <port> <data_dir>")
	fmt.Println("  cloudnest start <port> <data_dir>")
	fmt.Println("  cloudnest version")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  ./cloudnest 8080 ./data")
	fmt.Println("  ./cloudnest start 8080 /var/lib/cloudnest")
}

func printVersion() {
	fmt.Printf("cloudnest version=%s commit=%s build_time=%s\n", buildVersion, buildCommit, buildTime)
}
