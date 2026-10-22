package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/service"
	storageMysql "cloudenest_go/internal/storage/mysql"
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
	args := os.Args[1:]
	maintenance, recognized, err := parseMaintenanceArgs(args)
	if err != nil {
		log.Fatal(err)
	}
	if recognized {
		args = nil
	}
	if err := applyCLIOverrides(&cfg, args); err != nil {
		log.Fatalf("启动参数错误: %v", err)
	}
	databaseOnly := maintenance.kind == "audit-file-names" || maintenance.kind == "migrate-file-names" || maintenance.kind == "audit-storage-references" || maintenance.kind == "migrate-storage-references"
	if databaseOnly {
		err = cfg.ValidateDatabase()
	} else {
		err = cfg.Validate()
	}
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}
	if maintenance.kind == "bootstrap-admin" {
		if err := store.ValidateBootstrapPassword(os.Getenv("CLOUDNEST_BOOTSTRAP_PASSWORD")); err != nil {
			log.Fatal(err)
		}
	}

	var resetPassword string
	if maintenance.kind == "reset-password" {
		info, err := os.Stdin.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 {
			log.Fatal("reset-password requires a private pipe/file on stdin; terminal echo input is refused")
		}
		resetPassword, err = readResetPassword(os.Stdin)
		if err != nil {
			log.Fatal(err)
		}
	}

	db, err := store.NewMySQL(cfg)
	if err != nil {
		log.Fatalf("初始化 MySQL 失败: %s", explainStartupError(err))
	}

	if maintenance.kind != "migrate" && !databaseOnly {
		if err := store.CheckAuthSchema(db); err != nil {
			log.Fatal(err)
		}
	}
	if maintenance.kind != "" {
		switch maintenance.kind {
		case "audit-file-names":
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			var report store.FileNameAudit
			report, err = store.AuditFileNames(ctx, db)
			if err == nil {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				err = encoder.Encode(report)
				if err == nil && report.HasFindings() {
					err = store.ErrFileNameAuditFindings
				}
			}
		case "audit-storage-references":
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			var report store.StorageReferenceAudit
			report, err = store.AuditStorageReferences(ctx, db)
			if err == nil {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				err = encoder.Encode(report)
				if err == nil && report.HasFindings() {
					err = store.ErrStorageReferenceFindings
				}
			}
		case "migrate-storage-references":
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			err = store.MigrateStorageReferences(ctx, db)
		case "migrate-file-names":
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			err = store.MigrateFileNames(ctx, db)
		case "migrate":
			err = store.Migrate(db)
		case "bootstrap-admin":
			err = store.BootstrapAdminUser(db, os.Getenv("CLOUDNEST_BOOTSTRAP_PASSWORD"))
		case "reset-password":
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			svc := service.NewAuthService(storageMysql.NewAuthRepo(db), nil, cfg)
			err = svc.ResetPasswordForOperator(ctx, maintenance.userID, maintenance.username, resetPassword)
		}
		if err != nil {
			log.Fatalf("维护命令失败: %s", explainStartupError(err))
		}
		log.Printf("维护命令 %s 完成", maintenance.kind)
		return
	}
	if err := store.CheckFileSchema(db); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("准备数据目录失败: %v", err)
	}

	rdb, err := store.NewRedis(context.Background(), cfg)
	if err != nil {
		log.Fatalf("初始化 Redis 失败: %s", explainStartupError(err))
	}

	container := app.NewContainer(cfg, db, rdb)
	r := container.NewRouter()

	addr := fmt.Sprintf(":%s", cfg.AppPort)
	log.Printf("服务启动成功，监听地址: %s，数据目录: %s", addr, cfg.DataDir)
	server := &http.Server{Addr: addr, Handler: r, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	fmt.Println("  cloudnest reset-password <用户ID> <精确用户名>   从标准输入读取新密码并撤销该用户全部会话")
	fmt.Println("  cloudnest audit-file-names      只读 JSON 名称冲突审计；发现问题时非零退出，不修改数据")
	fmt.Println("  cloudnest migrate-file-names    审计通过后显式安装版本化活动文件名唯一约束（先备份并停旧实例）")
	fmt.Println("  cloudnest audit-storage-references   只读 JSON 存储引用审计；包括回收站与旧版本，不读取或删除物理文件")
	fmt.Println("  cloudnest migrate-storage-references 显式安装文件/版本到存储的 RESTRICT 外键（先备份并停旧实例）")
	fmt.Println("  cloudnest migrate              显式应用模型结构（先备份数据库）")
	fmt.Println("  cloudnest bootstrap-admin      仅初始化空用户表，密码从 CLOUDNEST_BOOTSTRAP_PASSWORD 读取")
	fmt.Println("")
	fmt.Println("示例:")
	fmt.Println("  ./cloudnest 8080 ./data")
	fmt.Println("  ./cloudnest start 8080 /var/lib/cloudnest")
}

func printVersion() {
	fmt.Printf("cloudnest version=%s commit=%s build_time=%s\n", buildVersion, buildCommit, buildTime)
}
