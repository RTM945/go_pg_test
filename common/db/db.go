package db

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"

	"go_pg_test/common/dispatch"
	"pdbgen/dbpool"
)

// 业务访问数据库只走这里：事务和咨询锁、只读查询，以及把 pdbgen 的错误分成客户端能处理的几类。
//
// 单 bigint 的 key 空间只给 uid 用（WithUserLock / WithUserTryLock），pdb.xml 里各表的 lock 列
// 就是按它核对的：改哪个玩家的行，事务就得持有哪个玩家的锁。其他业务锁用命名锁
// （双 int32：命名空间和 key 各哈希一次）；两个 key 空间在 PostgreSQL 里互不重叠。

var (
	// ErrBusy 同一个用户（或同一把命名锁）的上一个请求还占着锁：try 锁拿不到，或者阻塞锁等到了
	// lock_timeout。不是服务端的错，客户端稍后重试。
	ErrBusy = dispatch.NewClientError(dispatch.Conflict, "REQ_IN_FLIGHT", "previous request is still in flight")

	// ErrUnavailable 数据库这次没在预算（transactionTimeoutMs）内做完，或者 COMMIT 发出去之后连接断了、
	// 不知道提交了没有。客户端稍后原样重试：真的已经提交了的话，重试会被 requestToken 链拒绝
	// （REQ_TOKEN_STALE），客户端照常免登录、重新同步；免登录的重试按宽限期内的旧 refreshToken 处理。
	ErrUnavailable = dispatch.NewClientError(dispatch.Unavailable, "BUSY", "database unavailable")
)

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

// Query 只读查询，不开事务、不拿锁，查出来的行不能改。错误的分类和事务一样。
func Query(ctx context.Context, fn func(context.Context) error) error {
	return classify(dbpool.Query(ctx, fn))
}

func procedure(ctx context.Context, lock dbpool.LockSpec, fn func(context.Context) error) error {
	return classify(dbpool.Procedure(ctx, dbpool.ProcedureOptions{Lock: lock}, fn))
}

// classify 把 pdbgen 认得出的、客户端稍后重试就行的失败包成 ClientError（日志里还有原始错误）。
// fn 自己返回的错误原样透传；其他错误（SQL 写错、违反约束、连不上库）是服务端的问题，回 500。
func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, dbpool.ErrAdvisoryLockNotAcquired):
		return fmt.Errorf("%w: %w", ErrBusy, err)
	case errors.Is(err, dbpool.ErrTimeout), errors.Is(err, dbpool.ErrCommitUnknown):
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}

func hash32(s string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int32(h.Sum32())
}
