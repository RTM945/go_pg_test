package login

import (
	"context"
	"errors"
	"fmt"
	"go_pg_test/app/test/msg"
	"go_pg_test/app/test/ptable"
	appsession "go_pg_test/app/test/session"
	"go_pg_test/common/db"
	"go_pg_test/common/dispatch"
	"go_pg_test/common/redis"
	"go_pg_test/common/token"
	"go_pg_test/io"
	"pdbgen/dbpool"
	"time"
)

const (
	extToken     int32 = 1
	extChannel   int32 = 2
	extPackageID int32 = 3
	extPreID     int32 = 4
	extUDID      int32 = 5

	refreshGrace = time.Minute // 轮换后，旧 token 仍可用一小段时间

	refreshTTL  = 30 * 24 * time.Hour
	maxFieldLen = 128
)

var (
	ErrTokenInvalid  = errors.New("refresh token invalid")  // -> 401 TOKEN_INVALID，客户端回到 SDK 登录
	ErrLoginFailed   = errors.New("login failed")           // SDK 校验不通过 -> 401 LOGIN_FAILED
	ErrAccountBanned = errors.New("account banned")         // -> 403 ACCOUNT_BANNED
	ErrPlatBusy      = errors.New("platform unavailable")   // 平台接口超时/故障 -> 503 BUSY
	ErrClientTooOld  = errors.New("client version too old") // -> 426 CLIENT_TOO_OLD
)

// loginOutcome 登录成功后要下发的所有东西。
// loginToken / requestToken 写到业务 session，refreshToken 放进 LoginResult。
type loginOutcome struct {
	uid          int64
	name         string
	loginToken   string
	refreshToken string
	requestToken string
}

func (o loginOutcome) write(s io.Session) error {
	appSession, ok := s.(*appsession.Session)
	if !ok {
		return fmt.Errorf("login: session %T cannot carry tokens", s)
	}
	appSession.SetNextLoginToken(o.loginToken)
	appSession.SetNextReqToken(o.requestToken)

	resp := msg.NewLoginResult()
	resp.Uid = o.uid
	resp.Name = o.name
	resp.RefreshToken = o.refreshToken
	return s.Write(resp)
}

// 三个 token：
//
//	loginToken    Redis，鉴权用，登录 / 免登录后轮换；响应头 X-Login-Token 下发
//	refreshToken  DB，免登录用，按设备一行；轮换后旧值在宽限期内仍可用
//	requestToken  DB user.request_token，单次链：事务内比对，成功后轮换；
//	              响应头 X-Request-Token 下发，客户端下次请求原样带回
//
// loginToken / requestToken 走 header，只有 refreshToken 是 LoginResult 的业务字段。
func ProcessLogin(ctx context.Context, req *msg.Login) error {
	deviceID := req.Ext[extUDID]
	// 用于自动登录的token
	refreshToken := req.Ext[extToken]
	if req.Plat == "" || req.Account == "" || deviceID == "" {
		return fmt.Errorf("%w: bad login params", dispatch.ErrMalformedPayload)
	}
	if err := checkClientVersion(req.ClientVersion); err != nil {
		return err
	}
	v, ok := verifiers[req.Plat]
	if !ok {
		return fmt.Errorf("%w: unsupported plat %q", dispatch.ErrMalformedPayload, req.Plat)
	}
	// TODO: 按 IP / account 限流

	if refreshToken != "" {
		// 有传refreshToken 自动登录 一定是老用户
		return autoLogin(ctx, req, v, deviceID, refreshToken)
	}
	// 手动登录
	return sdkLogin(ctx, req, v, deviceID)
}

func sdkLogin(ctx context.Context, req *msg.Login, v PlatformVerifier, deviceID string) error {
	// 事务外：向平台校验凭证
	platUID, err := v.Verify(ctx, req.Account, req.Ext)
	if err != nil {
		return err
	}
	platform := v.PlatformID()

	refreshToken := token.New32()
	refreshExp := time.Now().Add(refreshTTL)
	requestToken := token.New32()

	uid, created, name, err := findOrCreateAccount(ctx, platform, platUID, deviceID, refreshToken, refreshExp, requestToken)
	if err != nil {
		return err
	}

	// 老玩家（含排队期间别人刚建好的）：另起事务加 uid 锁，
	// 顺便开一条新的 requestToken 链（登录 = 新会话，旧链作废）。
	if !created {
		err = db.WithUserLock(ctx, uid, func(ctx context.Context) error {
			user := ptable.UserTable.GetById(ctx, uid)
			if user == nil {
				return fmt.Errorf("user %d not found", uid)
			}
			if user.Status() != 0 {
				return ErrAccountBanned
			}
			name = user.Name()
			upsertRequestToken(ctx, uid, requestToken)
			upsertRefreshToken(ctx, uid, deviceID, refreshToken, refreshExp)
			return nil
		})
		if err != nil {
			return err
		}
	}

	// 事务已提交，再发登录 token（顶号发生在这里）
	loginToken, err := redis.IssueLoginToken(ctx, uid, deviceID)
	if err != nil {
		return err
	}
	return loginOutcome{
		uid:          uid,
		name:         name,
		loginToken:   loginToken,
		refreshToken: refreshToken,
		requestToken: requestToken,
	}.write(req.Session())
}

func autoLogin(ctx context.Context, req *msg.Login, v PlatformVerifier, deviceID, refreshToken string) error {
	// plat + account 找到 uid 不信任客户端传的uid
	uid, err := findUIDByIdentity(ctx, v.PlatformID(), req.Account)
	if err != nil {
		return err
	}
	if uid == 0 {
		return ErrTokenInvalid
	}
	newRefreshToken := token.New32()
	newExp := time.Now().Add(refreshTTL)
	var name, requestToken string

	err = db.WithUserLock(ctx, uid, func(ctx context.Context) error {
		now := time.Now()
		userRefreshToken := ptable.UserRefreshTokenTable.GetByUidDeviceId(ctx, uid, deviceID)
		if userRefreshToken == nil || userRefreshToken.RevokedAt() != 0 || now.Unix() >= userRefreshToken.ExpiresAt() {
			return ErrTokenInvalid
		}

		keepPrev := false
		switch {
		case refreshToken == userRefreshToken.Token():
			// 正常
		case len(userRefreshToken.PrevToken()) > 0 &&
			refreshToken == userRefreshToken.PrevToken() &&
			now.Unix()-userRefreshToken.RotatedAt() <= int64(refreshGrace/time.Second):
			keepPrev = true // 上次的响应丢了，客户端手里还是旧 token
		default:
			return ErrTokenInvalid
		}

		// token 通过之后才检查封禁，避免没有凭证的人探测出账号状态
		user := ptable.UserTable.GetById(ctx, uid)
		if user == nil {
			return fmt.Errorf("user %d not found", uid)
		}
		if user.Status() != 0 {
			return ErrAccountBanned
		}
		name = user.Name()
		user.SetLastLoginAt(now.Unix())

		// 登录即新会话：requestToken 链从头开始
		requestToken = token.New32()
		upsertRequestToken(ctx, uid, requestToken)

		// 轮换：当前 token 挪到 prev（keepPrev 时 prev 保持不变）
		if !keepPrev {
			userRefreshToken.SetPrevToken(userRefreshToken.Token())
		}
		userRefreshToken.SetToken(newRefreshToken)
		userRefreshToken.SetRotatedAt(now.Unix())
		userRefreshToken.SetExpiresAt(newExp.Unix())

		// todo 登录成功 登录后业务
		// 不在这里发送业务，而是等待客户端主动发进入大厅协议
		return nil
	})
	if err != nil {
		return err
	}

	loginToken, err := redis.IssueLoginToken(ctx, uid, deviceID)
	if err != nil {
		return err
	}
	return loginOutcome{
		uid:          uid,
		name:         name,
		loginToken:   loginToken,
		refreshToken: newRefreshToken,
		requestToken: requestToken,
	}.write(req.Session())
}

// 返回 0 表示没有这个身份
func findUIDByIdentity(ctx context.Context, platform int16, platUID string) (uid int64, err error) {
	err = dbpool.Query(ctx, func(ctx context.Context) error {
		if ident := ptable.UserIdentityTable.SelectByPlatformPlatformUID(ctx, platform, platUID); ident != nil {
			uid = ident.Uid()
		}
		return nil
	})
	return uid, err
}

func findOrCreateAccount(ctx context.Context, platform int16, platUID, deviceID, refreshToken string, refreshExp time.Time, requestToken string) (int64, bool, string, error) {
	var err error
	var created bool
	var uid int64
	var name string
	// 1. 先无锁查，绝大多数登录在这里就结束
	err = dbpool.Query(ctx, func(ctx context.Context) error {
		if ident := ptable.UserIdentityTable.SelectByPlatformPlatformUID(ctx, platform, platUID); ident != nil {
			uid = ident.Uid()
		}
		return nil
	})
	if err != nil || uid != 0 {
		return uid, false, "", err
	}

	// 2. 没查到：按身份加 named lock，事务内再查一次，再建
	lockValue := db.HashLockNS(fmt.Sprintf("%d:%s", platform, platUID))
	err = db.WithNamedLock(ctx, "identity", lockValue, func(ctx context.Context) error {
		if ident := ptable.UserIdentityTable.GetByPlatformPlatformUID(ctx, platform, platUID); ident != nil {
			uid = ident.Uid() // 排队期间别人已经建好了
			return nil
		}

		now := time.Now().Unix()
		user := ptable.NewUser()
		user.SetCreatedAt(now)
		ptable.UserTable.Insert(ctx, user)
		newUID := user.Id()
		if newUID == 0 {
			return errors.New("insert user returned id 0")
		}

		ident := ptable.NewUserIdentity()
		ident.SetUid(newUID)
		ident.SetPlatform(platform)
		ident.SetPlatformUID(platUID)
		ident.SetCreatedAt(now)
		ptable.UserIdentityTable.Insert(ctx, ident)

		// 免登录行也要在这个事务里建，否则首次登录后没法免登录
		insertRefreshToken(ctx, newUID, deviceID, refreshToken, refreshExp)
		insertRequestToken(ctx, newUID, requestToken)
		uid, created = newUID, true
		name = user.Name()
		return nil
	})
	return uid, created, name, err
}

func checkClientVersion(v string) error {
	// TODO: 过旧返回 ErrClientTooOld
	return nil
}

// ---- 免登录 token 表 ----
// 调用方必须已持有 uid 锁；或者 uid 是刚建的新号（没有竞争）
func insertRefreshToken(ctx context.Context, uid int64, deviceID string, refreshToken string, exp time.Time) *ptable.UserRefreshToken {
	row := ptable.NewUserRefreshToken()
	row.SetUid(uid)
	row.SetDeviceId(deviceID)
	row.SetToken(refreshToken)
	row.SetRotatedAt(time.Now().Unix())
	row.SetExpiresAt(exp.Unix())
	ptable.UserRefreshTokenTable.Insert(ctx, row)
	return row
}

// SDK 登录：整行覆盖，清掉 prev 和作废标记。
// 没有 ON CONFLICT，所以先查后写；同一 (uid, device) 的写都在 uid 锁内，不会并发。
func upsertRefreshToken(ctx context.Context, uid int64, deviceID string, refreshToken string, exp time.Time) {
	row := ptable.UserRefreshTokenTable.GetByUidDeviceId(ctx, uid, deviceID)
	if row == nil {
		// 刚插入的行所有字段都已经是新值了，再 set 一遍只会多一条同数据 UPDATE。
		insertRefreshToken(ctx, uid, deviceID, refreshToken, exp)
		return
	}
	row.SetToken(refreshToken)
	row.SetPrevToken("") // 必须清，否则作废前的旧 token 在宽限期内还能用
	row.SetRotatedAt(time.Now().Unix())
	row.SetExpiresAt(exp.Unix())
	row.SetRevokedAt(0)
}

func insertRequestToken(ctx context.Context, uid int64, requestToken string) *ptable.UserRequestToken {
	row := ptable.NewUserRequestToken()
	row.SetUid(uid)
	row.SetToken(requestToken)
	ptable.UserRequestTokenTable.Insert(ctx, row)
	return row
}

func upsertRequestToken(ctx context.Context, uid int64, requestToken string) {
	row := ptable.UserRequestTokenTable.GetByUid(ctx, uid)
	if row == nil {
		insertRequestToken(ctx, uid, requestToken)
		return
	}
	row.SetToken(requestToken)
}
