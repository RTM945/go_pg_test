package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var rdb *redis.Client

func Init(addr, pwd string) error {
	if rdb != nil {
		return errors.New("redis already initialized")
	}

	c := redis.NewClient(&redis.Options{
		Addr:            addr,
		Password:        pwd,
		PoolSize:        32,
		MinIdleConns:    4,
		ConnMaxIdleTime: 5 * time.Minute,
		DialTimeout:     1 * time.Second,
		ReadTimeout:     500 * time.Millisecond,
		WriteTimeout:    500 * time.Millisecond,
		PoolTimeout:     1 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return fmt.Errorf("redis ping: %w", err)
	}

	rdb = c
	return nil
}

func Close() error {
	if rdb == nil {
		return nil
	}
	return rdb.Close()
}

// PoolStats 压测和线上监控用
func PoolStats() *redis.PoolStats {
	return rdb.PoolStats()
}
