package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	loadEnv()

	cfg := config.Load()
	if err := applyCLIOverrides(&cfg, os.Args[1:]); err != nil {
		log.Fatalf("启动参数错误: %v", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("准备数据目录失败: %v", err)
	}

	db, err := store.NewMySQL(cfg)
	if err != nil {
		log.Fatalf("初始化 MySQL 失败: %s", explainStartupError(err))
	}

	if err := store.SeedAdminUser(db); err != nil {
		log.Fatalf("初始化管理员账号失败: %s", explainStartupError(err))
	}

	rdb, err := store.NewRedis(context.Background(), cfg)
	if err != nil {
		log.Fatalf("初始化 Redis 失败: %s", explainStartupError(err))
	}

	container := app.NewContainer(cfg, db, rdb)
	r := container.NewRouter()

	addr := fmt.Sprintf(":%s", cfg.AppPort)
	log.Printf("服务启动成功，监听地址: %s，数据目录: %s", addr, cfg.DataDir)
	if err := r.Run(addr); err != nil {
		log.Fatalf("启动 HTTP 服务失败: %s", explainStartupError(err))
	}
}

func explainStartupError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(msg, "Error 1045") || strings.Contains(lower, "access denied for user"):
		return "数据库账号或密码错误（MySQL 拒绝访问），请检查 MYSQL_DSN。原始错误: " + msg
	case strings.Contains(lower, "connection refused"):
		return "连接被拒绝，请确认 MySQL/Redis 服务已启动并监听正确地址。原始错误: " + msg
	case strings.Contains(lower, "no such host"):
		return "无法解析数据库或 Redis 主机名，请检查配置。原始错误: " + msg
	case strings.Contains(lower, "unknown database"):
		return "数据库不存在，请先创建对应库。原始错误: " + msg
	case strings.Contains(lower, "noauth"):
		return "Redis 需要认证，请检查 REDIS_PASSWORD。原始错误: " + msg
	default:
		return msg
	}
}

func loadEnv() {
	// 优先读取当前工作目录，其次读取可执行文件所在目录，便于发布包直接运行。
	_ = godotenv.Load()

	exePath, err := os.Executable()
	if err != nil {
		return
	}
	exeEnv := filepath.Join(filepath.Dir(exePath), ".env")
	_ = godotenv.Overload(exeEnv)
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
		return errors.New("用法: cloudnest [start] [port] [data_dir]")
	}

	if len(args) >= 1 && args[0] != "" {
		if !isValidPort(args[0]) {
			return fmt.Errorf("端口无效: %s", args[0])
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
	fmt.Println("CloudNest 服务端")
	fmt.Println("用法:")
	fmt.Println("  cloudnest")
	fmt.Println("  cloudnest <端口> <数据目录>")
	fmt.Println("  cloudnest start <端口> <数据目录>")
	fmt.Println("  cloudnest version")
	fmt.Println("")
	fmt.Println("示例:")
	fmt.Println("  ./cloudnest 8080 ./data")
	fmt.Println("  ./cloudnest start 8080 /var/lib/cloudnest")
}

func printVersion() {
	fmt.Printf("cloudnest version=%s commit=%s build_time=%s\n", buildVersion, buildCommit, buildTime)
}
