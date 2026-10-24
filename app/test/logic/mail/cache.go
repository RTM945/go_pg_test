package mail

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"go_pg_test/app/test/ptable"
	"pdbgen/dbpool"
)

// Config 是 gm_mail 在内存中的只读快照，不持有 ptable 记录。
type Config struct {
	ID            int64
	Params        string
	AwardList     string
	StartAt       int64
	EndAt         int64
	Channel       string
	UserCreatedAt int64
	Condition     string
}

// Cache 是进程级的 gm_mail 配置缓存。
// 启动时 Load 一次；之后每次收到 GM 平台的全服通知，写库成功后 Reload。
type Cache struct {
	log     *slog.Logger
	mu      sync.Mutex
	configs []Config
}

func NewCache(log *slog.Logger) *Cache {
	return &Cache{log: log}
}

var processCache atomic.Pointer[Cache]

// SetCache 在 main 里装配，handler 通过 currentCache 读取。
func SetCache(c *Cache) {
	processCache.Store(c)
}

func currentCache() *Cache {
	return processCache.Load()
}

// Reload 从数据库重读 gm_mail。只能在事务外调用：
// 收到 GM 通知的 handler 已经写完自己的事务，main 启动时也没有事务。
func (c *Cache) Reload(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var rows []*ptable.GMMail
	err := dbpool.Query(ctx, func(ctx context.Context) error {
		rows = ptable.GmMailTable.SelectAll(ctx)
		return nil
	})
	if err != nil {
		return err
	}
	c.replace(rows)
	return nil
}

// Configs 返回当前快照。重载是替换整个 slice，不会修改正在被读取的旧 slice。
func (c *Cache) Configs() []Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.configs
}

// replace 调用方必须持有 c.mu。
func (c *Cache) replace(rows []*ptable.GMMail) {
	configs := make([]Config, 0, len(rows))
	for _, row := range rows {
		configs = append(configs, Config{
			ID:            row.Id(),
			Params:        row.Params(),
			AwardList:     row.AwardList(),
			StartAt:       row.StartAt(),
			EndAt:         row.EndAt(),
			Channel:       row.Channel(),
			UserCreatedAt: row.UserCreatedAt(),
			Condition:     row.Condition(),
		})
	}
	slices.SortFunc(configs, func(a, b Config) int {
		return cmp.Compare(a.ID, b.ID)
	})

	c.configs = configs
	c.log.Info("gm_mail cache reloaded", "count", len(configs))
}
