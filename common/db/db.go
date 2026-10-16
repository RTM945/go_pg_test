package db

import (
	"context"
	"hash/fnv"
	"pdbgen/dbpool"
)

func WithUserTryLock(ctx context.Context, uid int64, fn func(context.Context) error) error {
	return dbpool.WithTryAdvisoryLock(ctx, uid, fn)
}

func WithUserLock(ctx context.Context, uid int64, fn func(context.Context) error) error {
	return dbpool.WithAdvisoryLock(ctx, uid, fn)
}

func HashLockNS(name string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int32(h.Sum32())
}

func WithNamedLock(ctx context.Context, lockKey string, lockValue int32, fn func(context.Context) error) error {
	return dbpool.WithAdvisoryLock2(ctx, HashLockNS(lockKey), lockValue, fn)
}

func WithNamedTryLock(ctx context.Context, lockKey string, lockValue int32, fn func(context.Context) error) error {
	return dbpool.WithTryAdvisoryLock2(ctx, HashLockNS(lockKey), lockValue, fn)
}
