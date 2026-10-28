package db

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"

	"go_pg_test/common/dispatch"
	"pdbgen/dbpool"
)

// 咨询锁只从这里拿。单 bigint 的 key 空间只给 uid 用（WithUserLock / WithUserTryLock），
// 其他业务锁用命名锁（双 int32：命名空间和 key 各哈希一次）；两个 key 空间在 PostgreSQL 里互不重叠。

// ErrBusy 同一个用户（或同一把命名锁）的上一个请求还占着锁：try 锁拿不到，或者阻塞锁等到了
// lock_timeout。不是服务端的错，客户端稍后重试。
var ErrBusy = dispatch.NewClientError(dispatch.Conflict, "REQ_IN_FLIGHT", "previous request is still in flight")

// WithUserTryLock 玩家请求用：同一个 uid 的上一个请求还没完成时直接返回 ErrBusy。
func WithUserTryLock(ctx context.Context, uid int64, fn func(context.Context) error) error {
	return procedure(ctx, dbpool.TryAdvisoryLock(uid), fn)
}

// WithUserLock 登录、GM、RPC 用：等同一个 uid 的上一个请求完成，最多等 lock_timeout。
func WithUserLock(ctx context.Context, uid int64, fn func(context.Context) error) error {
	return procedure(ctx, dbpool.AdvisoryLock(uid), fn)
}

// WithNamedLock 按 (ns, key) 串行，两者各哈希成 int32。哈希冲突只会让无关的请求多排一次队。
func WithNamedLock(ctx context.Context, ns, key string, fn func(context.Context) error) error {
	return procedure(ctx, dbpool.AdvisoryLock2(hash32(ns), hash32(key)), fn)
}

func procedure(ctx context.Context, lock dbpool.LockSpec, fn func(context.Context) error) error {
	err := dbpool.Procedure(ctx, dbpool.ProcedureOptions{Lock: lock}, fn)
	if errors.Is(err, dbpool.ErrAdvisoryLockNotAcquired) {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return err
}

func hash32(s string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int32(h.Sum32())
}
