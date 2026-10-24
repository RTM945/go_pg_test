package event

import (
	"context"

	"go_pg_test/app/test/msg"
	"go_pg_test/common/redis"
	testpb "go_pg_test/genpb/test"
)

const (
	// EventTypeMailNew 有新的 GM/系统邮件，客户端发现版本变化后去拉 CMails。
	EventTypeMailNew int32 = 1
	// EventTypeActivityStart 活动开始，客户端发现版本变化后去同步活动状态。
	EventTypeActivityStart int32 = 2
)

// ProcessPollEvents 返回所有事件类型的当前版本号。
// 客户端本地保存一份 map，比较后只同步发生变化的类型：
//   - global 变大：全服事件（全服 GM 邮件、活动），所有在线客户端都会同步；
//   - user 变大：只针对本玩家的定向事件（单人 GM 邮件），其他玩家的版本不变，
//     因此不会去拉 CMails。
//
// 服务端不保存每用户游标；响应丢了客户端不更新本地 map，下次重新比较即可。
// 如果客户端发现本地 seq 大于服务端 seq，说明 Redis 被清空/重置过
// （定向版本的 key 过期也会这样），应按“该类型需要全量同步”处理，
// 然后接受服务端的版本号。
func ProcessPollEvents(ctx context.Context, req *msg.PollEvents) error {
	versions, err := redis.EventVersions(ctx, req.Session().UID())
	if err != nil {
		return err
	}

	resp := msg.NewEventVersions()
	resp.Versions = make(map[int32]*testpb.EventVersion, len(versions))
	for eventType, v := range versions {
		resp.Versions[eventType] = &testpb.EventVersion{
			Global: v.Global,
			User:   v.User,
		}
	}
	return req.Session().Write(resp)
}
