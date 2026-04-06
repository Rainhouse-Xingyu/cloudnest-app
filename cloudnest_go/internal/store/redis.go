package store

import (
	"context"
	"fmt"

	"cloudenest_go/internal/config"

	"github.com/redis/go-redis/v9"
)
//NewRedis函数用于创建一个新的Redis客户端连接，接受上下文和配置对象作为参数，返回Redis客户端对象和错误信息。函数首先使用redis.NewClient函数创建一个新的Redis客户端实例，并根据配置设置连接参数。然后调用Ping方法测试连接是否成功，如果失败则返回错误信息。最后返回成功的Redis客户端对象。
func NewRedis(ctx context.Context, cfg config.Config) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connect redis failed: %w", err)
	}

	return client, nil
}
