package db

import (
	"context"
	"hash/fnv"
	"pdbgen/dbpool"
)

func WithUserTryLock(ctx context.Context, uid int64, fn func(context.Context) error) error {
	return dbpool.Procedure(ctx, dbpool.ProcedureOptions{
		Lock: dbpool.TryAdvisoryLock(uid),
	}, fn)
}

func WithUserLock(ctx context.Context, uid int64, fn func(context.Context) error) error {
	return dbpool.Procedure(ctx, dbpool.ProcedureOptions{
		Lock: dbpool.AdvisoryLock(uid),
	}, fn)
}

func HashLockNS(name string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int32(h.Sum32())
}

func WithNamedLock(ctx context.Context, lockKey string, lockValue int32, fn func(context.Context) error) error {
	return dbpool.Procedure(ctx, dbpool.ProcedureOptions{
		Lock: dbpool.AdvisoryLock2(HashLockNS(lockKey), lockValue),
	}, fn)
}

func WithNamedTryLock(ctx context.Context, lockKey string, lockValue int32, fn func(context.Context) error) error {
	return dbpool.Procedure(ctx, dbpool.ProcedureOptions{
		Lock: dbpool.TryAdvisoryLock2(HashLockNS(lockKey), lockValue),
	}, fn)
}
