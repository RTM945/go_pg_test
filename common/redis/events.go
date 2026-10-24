package redis

import (
	"context"
	"strconv"
	"time"
)

const (
	// eventVersionsKey 全局 hash：field=事件类型 id，value=全服序号。
	eventVersionsKey = "event:versions"
	// eventUserKeyPrefix 定向事件：每个目标用户一个 hash，field=事件类型 id。
	// 只有收到定向事件的用户才会有这个 key，并且带 TTL。
	eventUserKeyPrefix = "event:user:"
	// eventUserTTL 定向版本保留多久。玩家离线超过这个时间后，
	// 版本 key 过期，下次登录走一次全量 Sync。
	eventUserTTL = 24 * time.Hour
)

type EventVersion struct {
	Global int64
	User   int64
}

func userEventKey(uid int64) string {
	return eventUserKeyPrefix + strconv.FormatInt(uid, 10)
}

// IncrGlobalEventVersion 全服某个事件类型发生变化，序号 +1。
func IncrGlobalEventVersion(ctx context.Context, eventType int32) (int64, error) {
	seq, err := rdb.HIncrBy(ctx, eventVersionsKey, strconv.FormatInt(int64(eventType), 10), 1).Result()
	if err != nil {
		return 0, unavailable(err)
	}
	return seq, nil
}

// IncrUserEventVersion 只针对某个用户的定向事件，序号 +1。
// 其他用户的版本不变，因此不会触发他们 Sync。
//
// HINCRBY 成功后事件已经可见，EXPIRE 只负责不让 key 永久堆积，所以不用 Lua
// 把两条命令做成原子操作。EXPIRE 万一失败，下一次定向事件会再续期；
// 最坏情况是留下一个只含一个字段的小 key。
func IncrUserEventVersion(ctx context.Context, uid int64, eventType int32) (int64, error) {
	key := userEventKey(uid)
	seq, err := rdb.HIncrBy(ctx, key, strconv.FormatInt(int64(eventType), 10), 1).Result()
	if err != nil {
		return 0, unavailable(err)
	}
	if err := rdb.Expire(ctx, key, eventUserTTL).Err(); err != nil {
		return seq, unavailable(err)
	}
	return seq, nil
}

// parseEventVersions 把 hash 的 field/value 解析成事件类型 → 序号。
// 解析不了的值跳过，不让一个脏字段影响整个响应。
func parseEventVersions(raw map[string]string) map[int32]int64 {
	out := make(map[int32]int64, len(raw))
	for k, v := range raw {
		eventType, err := strconv.ParseInt(k, 10, 32)
		if err != nil {
			continue
		}
		seq, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		out[int32(eventType)] = seq
	}
	return out
}

// EventVersions 合并全局版本和当前用户的定向版本。
// 两次 HGETALL 分开读；即使中间来了新事件，也只是这次响应早一拍或晚一拍，
// 版本号仍然是单调的，下次 Poll 就能看到。
func EventVersions(ctx context.Context, uid int64) (map[int32]EventVersion, error) {
	globalRaw, err := rdb.HGetAll(ctx, eventVersionsKey).Result()
	if err != nil {
		return nil, unavailable(err)
	}
	userRaw, err := rdb.HGetAll(ctx, userEventKey(uid)).Result()
	if err != nil {
		return nil, unavailable(err)
	}

	versions := make(map[int32]EventVersion)
	for eventType, seq := range parseEventVersions(globalRaw) {
		versions[eventType] = EventVersion{Global: seq}
	}
	for eventType, seq := range parseEventVersions(userRaw) {
		cur := versions[eventType]
		cur.User = seq
		versions[eventType] = cur
	}
	return versions, nil
}
