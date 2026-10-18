package main

import (
	"context"
	"fmt"

	"go_pg_test/app/test/ptable"
	"go_pg_test/common/db"
	"go_pg_test/common/server"
	"go_pg_test/common/token"
	"go_pg_test/io"
	"pdbgen/dbpool"
)

var processRegistry = server.NewProcessorRegistry()

// processModes 记每个协议怎么跑事务；handler 用它判断要不要验 loginToken。
var processModes = map[int32]processMode{}

type processMode int

const (
	processQuery processMode = iota
	processUserTryLock
	processUserLock
	processNamedTryLock
	processNamedLock
	processNone // 不开事务不加锁，handler 自己管理
)

// register 把“反序列化 + 事务/锁 + requestToken 链”包进一个 DispatchFunc，
// 再登记进纯分发的 ProcessorRegistry。生成器只负责调用这个函数。
func register[T io.Msg](
	reg *server.ProcessorRegistry,
	typeId int32,
	newMsg func() T,
	fn func(ctx context.Context, req T) error,
	mode processMode,
) {
	processModes[typeId] = mode

	reg.Register(typeId, func(_ context.Context, typeId int32, buffer []byte, s io.Session) error {
		msg := newMsg()
		if err := msg.Read(buffer); err != nil {
			return fmt.Errorf("%w: typeId %d: %w", server.ErrMalformedPayload, typeId, err)
		}
		msg.SetSession(s)

		process := func(ctx context.Context) error {
			return fn(ctx, msg)
		}

		// db!
		ctx := context.Background()
		switch mode {
		case processQuery:
			return dbpool.Query(ctx, process)
		case processUserTryLock:
			return db.WithUserTryLock(ctx, s.UID(), func(ctx context.Context) error {
				return runUserProcess(ctx, s, process)
			})
		case processUserLock:
			return db.WithUserLock(ctx, s.UID(), func(ctx context.Context) error {
				return runUserProcess(ctx, s, process)
			})
		case processNone:
			return process(ctx)
		default:
			// todo processNamedTryLock 和 processNamedLock 基本用不到
			return fmt.Errorf("unknown process mode %d for typeId %d", mode, typeId)
		}
	})
}

// runUserProcess 在用户事务内维护 requestToken 单次链：
// 比对 → 业务 → 换新 token 并写响应头。
//
// 直接用 SQL 读写 user.request_token，而不是 ptable.User：业务代码很可能也会
// GetById 同一个 uid，UnitOfWork 发现同一事务里同一行有两个对象会 panic。
func runUserProcess(ctx context.Context, s io.Session, process func(context.Context) error) error {
	appSession, ok := s.(*session)
	if !ok {
		return fmt.Errorf("unexpected session type %T", s)
	}

	userRequestToken := ptable.UserRequestTokenTable.GetByUid(ctx, s.UID())
	if userRequestToken == nil || userRequestToken.Token() != appSession.reqToken {
		return server.ErrStaleRequestToken
	}

	if err := process(ctx); err != nil {
		return err
	}

	newToken := token.New32()
	userRequestToken.SetToken(newToken)

	// 事务还没提交，但 header 只是内存里的 map；提交失败时 handler 会把它删掉。
	s.Writer().Header().Set(hdrReqToken, newToken)
	return nil
}
