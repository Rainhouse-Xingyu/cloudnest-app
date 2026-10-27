package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

/*
Config结构体定义了应用程序的配置项，包括AppPort、MySQLDSN、RedisAddr、RedisPassword、RedisDB、JWTSecret和JWTExpireHours等字段。
Load函数从环境变量加载配置项，并提供默认值。JWTTTL方法计算JWT的过期时间，getEnv和getEnvInt函数用于获取环境变量的值并提供默认值。
*/
type Config struct {
	TransferMaintenanceDSN string
	BindAddr               string
	AppPort                string
	DataDir                string
	MySQLDSN               string
	RedisAddr              string
	RedisPassword          string
	RedisDB                int
	JWTSecret              string
	JWTExpireHours         int
	AIOllamaBaseURL        string
	AIOllamaModel          string
	AIEmbeddingModel       string
	AIPDFBinDir            string
	AIOCRModel             string
	AIGuideDocPath         string
	AITimeoutSec           int
	TransferTempBytes      int64
}

// MySQL、Redis、JWT等配置项，提供默认值并从环境变量加载
func Load() Config {
	jwtExpireHours := 168
	if raw := os.Getenv("JWT_EXPIRE_HOURS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			// Zero is rejected by Validate; never silently widen a mistyped TTL.
			jwtExpireHours = 0
		} else {
			jwtExpireHours = parsed
		}
	}
	return Config{
		TransferMaintenanceDSN: os.Getenv("TRANSFER_MAINTENANCE_DSN"),
		BindAddr:               getEnv("APP_BIND_ADDR", ""), TransferTempBytes: transferSeedLimit(),
		AppPort:          getEnv("APP_PORT", "8080"),
		DataDir:          getEnv("DATA_DIR", "data"),
		MySQLDSN:         os.Getenv("MYSQL_DSN"),
		RedisAddr:        getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:    getEnv("REDIS_PASSWORD", ""),
		RedisDB:          getEnvInt("REDIS_DB", 0),
		JWTSecret:        os.Getenv("JWT_SECRET"),
		JWTExpireHours:   jwtExpireHours,
		AIOllamaBaseURL:  getEnv("AI_OLLAMA_BASE_URL", "http://127.0.0.1:11434"),
		AIOllamaModel:    getEnv("AI_OLLAMA_MODEL", "qwen3-vl:4b"),
		AIPDFBinDir:      os.Getenv("AI_PDF_BIN_DIR"),
		AIOCRModel:       getEnv("AI_OCR_MODEL", "qwen3-vl:4b"),
		AIEmbeddingModel: getEnv("AI_EMBEDDING_MODEL", "qwen3-embedding:0.6b"),
		AIGuideDocPath:   getEnv("AI_GUIDE_DOC_PATH", "../../项目功能汇总.md"),
		AITimeoutSec:     getEnvInt("AI_TIMEOUT_SEC", 120),
	}
}

// Validate fails before opening databases or creating application data. Never
// include configuration values in errors: DSNs and signing keys are secrets.
func (c Config) Validate() error {
	if c.TransferTempBytes < 0 || c.TransferTempBytes > 4<<40 {
		return fmt.Errorf("TRANSFER_TEMP_LIMIT_BYTES must be a positive integer no larger than 4 TiB")
	}
	secret := strings.TrimSpace(c.JWTSecret)
	if len(secret) < 32 || secret != c.JWTSecret || strings.Contains(strings.ToLower(secret), "replace_with") || strings.Contains(strings.ToLower(secret), "change-me") {
		return fmt.Errorf("JWT_SECRET must be a non-placeholder random secret of at least 32 bytes without surrounding whitespace")
	}
	if err := c.ValidateDatabase(); err != nil {
		return err
	}
	if c.JWTExpireHours <= 0 || c.JWTExpireHours > 8760 {
		return fmt.Errorf("JWT_EXPIRE_HOURS must be between 1 and 8760")
	}
	return nil
}

func transferSeedLimit() int64 {
	raw := os.Getenv("TRANSFER_TEMP_LIMIT_BYTES")
	if raw == "" {
		return 8 << 30
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > 4<<40 {
		return -1
	}
	return value
}

// ValidateDatabase is sufficient for read-only maintenance that neither serves
// HTTP nor reads/writes sessions. It does not need the server signing key.
func (c Config) ValidateDatabase() error {
	if strings.TrimSpace(c.MySQLDSN) == "" || strings.Contains(c.MySQLDSN, "CHANGE_ME") {
		return fmt.Errorf("MYSQL_DSN must be explicitly configured")
	}
	return nil
}

// JWT计算过期时间，返回time.Duration的值
func (c Config) JWTTTL() time.Duration {
	return time.Duration(c.JWTExpireHours) * time.Hour
}

// 获取环境变量的值，不存在则返回默认值
func getEnv(key string, defaultValue string) string {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	return v
}

// 获取环境变量的整数值，不存在返回默认，转换失败亦是。
func getEnvInt(key string, defaultValue int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue
	}
	return n
}
