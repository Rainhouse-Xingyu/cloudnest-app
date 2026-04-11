package config

import (
	"os"
	"strconv"
	"time"
)

/*
Config结构体定义了应用程序的配置项，包括AppPort、MySQLDSN、RedisAddr、RedisPassword、RedisDB、JWTSecret和JWTExpireHours等字段。
Load函数从环境变量加载配置项，并提供默认值。JWTTTL方法计算JWT的过期时间，getEnv和getEnvInt函数用于获取环境变量的值并提供默认值。
*/
type Config struct {
	AppPort         string
	DataDir         string
	MySQLDSN        string
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	JWTSecret       string
	JWTExpireHours  int
	AIOllamaBaseURL string
	AIOllamaModel   string
	AIGuideDocPath  string
	AITimeoutSec    int
}

// MySQL、Redis、JWT等配置项，提供默认值并从环境变量加载
func Load() Config {
	return Config{
		AppPort:         getEnv("APP_PORT", "8080"),
		DataDir:         getEnv("DATA_DIR", "data"),
		MySQLDSN:        getEnv("MYSQL_DSN", "root:root@tcp(127.0.0.1:3306)/cloudnest?charset=utf8mb4&parseTime=True&loc=Local"),
		RedisAddr:       getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:   getEnv("REDIS_PASSWORD", ""),
		RedisDB:         getEnvInt("REDIS_DB", 0),
		JWTSecret:       getEnv("JWT_SECRET", "change-me-in-production"),
		JWTExpireHours:  getEnvInt("JWT_EXPIRE_HOURS", 168), // 7 days
		AIOllamaBaseURL: getEnv("AI_OLLAMA_BASE_URL", "http://127.0.0.1:11434"),
		AIOllamaModel:   getEnv("AI_OLLAMA_MODEL", "qwen3-vl:4b"),
		AIGuideDocPath:  getEnv("AI_GUIDE_DOC_PATH", "../../项目功能汇总.md"),
		AITimeoutSec:    getEnvInt("AI_TIMEOUT_SEC", 120),
	}
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
