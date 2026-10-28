package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"go_pg_test/common/dispatch"
	"go_pg_test/common/token"
)

// rdb 用你之前 Init 里的那个包级变量

const (
	loginTTL       = 2 * time.Hour  // 滑动过期
	loginMaxAge    = 24 * time.Hour // 绝对上限
	loginOfTTL     = loginMaxAge + time.Hour
	requestTTL     = 60 * time.Second // 要大于单个请求的最长处理时间
	maxReqTokenLen = 64
	kicked         = "KICKED"
)

var (
	ErrTokenExpired     = dispatch.NewClientError(dispatch.Unauthorized, "TOKEN_EXPIRED", "login token expired")
	ErrKicked           = dispatch.NewClientError(dispatch.Unauthorized, "KICKED", "kicked by another device")
	ErrRedisUnavailable = dispatch.NewClientError(dispatch.Unavailable, "BUSY", "redis unavailable") // 不能说成过期，否则会引发免登录风暴
	// ErrInvalidRequestToken 请求没带 requestToken，或者长度不对。
	ErrInvalidRequestToken = dispatch.NewClientError(dispatch.BadRequest, "REQ_TOKEN_INVALID", "invalid request token")
)

func unavailable(err error) error {
	return fmt.Errorf("%w: %w", ErrRedisUnavailable, err)
}

// ---- key 集中在这里 ----

// loginKey 只放登录 token 的哈希。login_of:<uid> 的值就是这个 key，
// 放明文的话，能读 Redis 的人就能拿它冒充任何在线玩家。
func loginKey(loginToken string) string { return "login:" + token.Hash(loginToken) }
func loginOfKey(uid int64) string       { return "login_of:" + strconv.FormatInt(uid, 10) }
func requestKey(uid int64, reqToken string) string {
	return "req:" + strconv.FormatInt(uid, 10) + ":" + reqToken
}

// ---- 登录 token ----

// KEYS[1]=新登录key KEYS[2]=login_of
// ARGV: 1=value 2=登录ttl秒 3=login_of ttl秒 4=KICKED标记
var issueScript = redis.NewScript(`
local old = redis.call('GET', KEYS[2])
if old and old ~= KEYS[1] and redis.call('EXISTS', old) == 1 then
  redis.call('SET', old, ARGV[4], 'KEEPTTL')
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
redis.call('SET', KEYS[2], KEYS[1], 'EX', ARGV[3])
return 1
`)

// IssueLoginToken 在登录 / 免登录事务提交之后调用。旧 token 被标记为 KICKED（顶号）。
func IssueLoginToken(ctx context.Context, uid int64, deviceID string) (string, error) {
	loginToken := token.New32()
	// device 放最后，即使里面含 '|' 也能正确解析
	val := fmt.Sprintf("%d|%d|%s", uid, time.Now().Unix(), deviceID)
	err := issueScript.Run(ctx, rdb,
		[]string{loginKey(loginToken), loginOfKey(uid)},
		val, int(loginTTL.Seconds()), int(loginOfTTL.Seconds()), kicked,
	).Err()
	if err != nil {
		return "", unavailable(err)
	}
	return loginToken, nil
}

// 读取并续期；KICKED 标记不续期。ARGV: 1=ttl秒 2=KICKED标记
var verifyScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if not v then return false end
if v ~= ARGV[2] then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
return v
`)

func VerifyLoginToken(ctx context.Context, loginToken string) (int64, error) {
	if loginToken == "" {
		return 0, ErrTokenExpired
	}
	key := loginKey(loginToken)
	v, err := verifyScript.Run(ctx, rdb, []string{key}, int(loginTTL.Seconds()), kicked).Text()
	if errors.Is(err, redis.Nil) {
		return 0, ErrTokenExpired
	}
	if err != nil {
		return 0, unavailable(err)
	}
	if v == kicked {
		return 0, ErrKicked
	}

	parts := strings.SplitN(v, "|", 3) // uid|created|device
	if len(parts) != 3 {
		return 0, ErrTokenExpired
	}
	uid, err1 := strconv.ParseInt(parts[0], 10, 64)
	created, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, ErrTokenExpired
	}
	if time.Since(time.Unix(created, 0)) > loginMaxAge {
		_ = rdb.Del(ctx, key).Err() // 尽力删除，失败也无所谓
		return 0, ErrTokenExpired
	}
	return uid, nil
}

var revokeScript = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
if old then redis.call('DEL', old) end
redis.call('DEL', KEYS[1])
return 1
`)

// RevokeLoginTokens 安全事件：直接删除，客户端会得到 TOKEN_EXPIRED，免登录再被拒，最终回到 SDK 登录
func RevokeLoginTokens(ctx context.Context, uid int64) error {
	if err := revokeScript.Run(ctx, rdb, []string{loginOfKey(uid)}).Err(); err != nil {
		return unavailable(err)
	}
	return nil
}

// ---- 请求幂等 ----

// TryAcquireRequest 返回 false 表示重复请求
func TryAcquireRequest(ctx context.Context, uid int64, reqToken string) (bool, error) {
	if reqToken == "" || len(reqToken) > maxReqTokenLen {
		return false, ErrInvalidRequestToken
	}
	ok, err := rdb.SetNX(ctx, requestKey(uid, reqToken), 1, requestTTL).Result()
	if err != nil {
		return false, unavailable(err)
	}
	return ok, nil
}

// ReleaseRequest 事务没提交成功时调用，允许客户端用同一个 token 重试
func ReleaseRequest(ctx context.Context, uid int64, reqToken string) error {
	if reqToken == "" || len(reqToken) > maxReqTokenLen {
		return nil
	}
	if err := rdb.Del(ctx, requestKey(uid, reqToken)).Err(); err != nil {
		return unavailable(err)
	}
	return nil
}
