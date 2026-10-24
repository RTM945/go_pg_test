package heartbeat

import (
	"context"

	"go_pg_test/app/test/msg"
)

// ProcessHeartbeat 不写任何响应：HTTP 200 本身就是心跳成功的信号。
// 认证和 Redis TTL 续期在 handler 里已经完成。
//
// 事件变化由客户端通过 PollEvents 拉取版本号，不在心跳里发送。
func ProcessHeartbeat(_ context.Context, _ *msg.Heartbeat) error {
	return nil
}
