package login

import (
	"context"
	"fmt"
	"time"

	"go_pg_test/app/test/msg"
	"go_pg_test/app/test/ptable"
	appsession "go_pg_test/app/test/session"
	"go_pg_test/common/db"
	"go_pg_test/common/dispatch"
	"go_pg_test/common/redis"
	"go_pg_test/common/token"
	"go_pg_test/io"
	"pdbgen/dbpool"
)

const (
	extToken     int32 = 1
	extChannel   int32 = 2
	extPackageID int32 = 3
	extPreID     int32 = 4
	extUDID      int32 = 5

	// refreshGrace 轮换后旧 token 还能用多久（上一次的响应丢了，客户端手里还是旧的）。
	// 从旧 token 被换下来的那一刻算起，这期间再用旧 token 不会续期。
	refreshGrace = time.Minute
	refreshTTL   = 30 * 24 * time.Hour

	// maxFieldLen 登录参数的长度上限：这些值会进数据库、索引和 Redis。
	maxFieldLen = 128
)

var (
	ErrTokenInvalid  = dispatch.NewClientError(dispatch.Unauthorized, "TOKEN_INVALID", "refresh token invalid") // 客户端回到 SDK 登录
	ErrLoginFailed   = dispatch.NewClientError(dispatch.Unauthorized, "LOGIN_FAILED", "login failed")           // SDK 校验不通过
	ErrAccountBanned = dispatch.NewClientError(dispatch.Forbidden, "ACCOUNT_BANNED", "account banned")
	ErrPlatBusy      = dispatch.NewClientError(dispatch.Unavailable, "BUSY", "platform unavailable") // 平台接口超时/故障
	ErrClientTooOld  = dispatch.NewClientError(dispatch.UpgradeRequired, "CLIENT_TOO_OLD", "client version too old")
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
//	refreshToken  DB，免登录用，按设备一行；免登录只凭它找人；轮换后旧值在宽限期内仍可用
//	requestToken  DB user.request_token，单次链：事务内比对，成功后轮换；
//	              响应头 X-Request-Token 下发，客户端下次请求原样带回
//
// loginToken / requestToken 走 header，只有 refreshToken 是 LoginResult 的业务字段。
// loginToken、refreshToken 落地只存 token.Hash，Redis 或库泄露了也冒充不了玩家；
// requestToken 单独拿到过不了鉴权，所以存明文。
//
// account（SDK 登录）和 refreshToken（免登录）恰好带一个，带哪个就走哪条路。
func ProcessLogin(ctx context.Context, req *msg.Login) error {
	deviceID := req.Ext[extUDID]
	refreshToken := req.Ext[extToken] // 用于自动登录的 token
	if err := checkLoginParams(req.Plat, req.Account, deviceID, refreshToken, req.ClientVersion); err != nil {
		return err
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
		return autoLogin(ctx, req, deviceID, refreshToken)
	}
	return sdkLogin(ctx, req, v, deviceID)
}

// checkLoginParams 登录参数都会进数据库、索引或 Redis：必填的不能为空，都不能超过 maxFieldLen。
// account 和 refreshToken 两个都带就分不清要走哪条路，和两个都不带一样直接拒绝。
func checkLoginParams(plat, account, deviceID, refreshToken, clientVersion string) error {
	if plat == "" || deviceID == "" {
		return fmt.Errorf("%w: plat and device id are required", dispatch.ErrMalformedPayload)
	}
	if (account == "") == (refreshToken == "") {
		return fmt.Errorf("%w: exactly one of account (sdk login) and refresh token (auto login)", dispatch.ErrMalformedPayload)
	}
	for _, v := range []string{plat, account, deviceID, refreshToken, clientVersion} {
		if len(v) > maxFieldLen {
			return fmt.Errorf("%w: login field longer than %d bytes", dispatch.ErrMalformedPayload, maxFieldLen)
		}
	}
	return nil
}

// startSession 是所有登录路径（新号、SDK 登录老号、免登录）成功时对 user 做的事：
// 记登录时间，开一条新的 requestToken 链（登录 = 新会话，旧链作废）。
// 调用方必须持有 uid 锁，或者 user 是本事务刚建的新号。
func startSession(user *ptable.User, now time.Time) (requestToken string) {
	requestToken = token.New32()
	user.SetLastLoginAt(now.Unix())
	user.SetRequestToken(requestToken)
	return requestToken
}

func sdkLogin(ctx context.Context, req *msg.Login, v PlatformVerifier, deviceID string) error {
	// 事务外：向平台校验凭证
	platUID, err := v.Verify(ctx, req.Account, req.Ext)
	if err != nil {
		return err
	}

	now := time.Now()
	out := loginOutcome{refreshToken: token.New32()}
	created, err := findOrCreateAccount(ctx, v.PlatformID(), platUID, deviceID, now, &out)
	if err != nil {
		return err
	}

	// 老玩家（含排队期间别人刚建好的）：另起事务加 uid 锁。
	if !created {
		err = db.WithUserLock(ctx, out.uid, func(ctx context.Context) error {
			user := ptable.UserTable.GetById(ctx, out.uid)
			if user == nil {
				return fmt.Errorf("user %d not found", out.uid)
			}
			if user.Status() != 0 {
				return ErrAccountBanned
			}
			out.name = user.Name()
			out.requestToken = startSession(user, now)
			upsertRefreshToken(ctx, out.uid, deviceID, out.refreshToken, now)
			return nil
		})
		if err != nil {
			return err
		}
	}

	// 事务已提交，再发登录 token（顶号发生在这里）
	if out.loginToken, err = redis.IssueLoginToken(ctx, out.uid, deviceID); err != nil {
		return err
	}
	return out.write(req.Session())
}

// autoLogin 只凭 refreshToken 找人：按它的哈希找到是哪一行，也就知道了要锁哪个 uid。
// 找行在事务外、不加锁，所以锁内要按主键重读，再校验一遍。
func autoLogin(ctx context.Context, req *msg.Login, deviceID, presented string) error {
	hash := token.Hash(presented)
	rowID, uid, err := findRefreshRow(ctx, hash)
	if err != nil {
		return err
	}
	if rowID == 0 {
		return ErrTokenInvalid
	}

	now := time.Now()
	out := loginOutcome{uid: uid, refreshToken: token.New32()}
	err = db.WithUserLock(ctx, uid, func(ctx context.Context) error {
		// 排队期间这一行可能已经被轮换、作废，或者被 SDK 登录覆盖。
		// 设备也要对得上：token 只属于当初拿到它的那台设备。
		row := ptable.UserRefreshTokenTable.GetById(ctx, rowID)
		if row == nil || row.DeviceId() != deviceID || row.RevokedAt() != 0 || now.Unix() >= row.ExpiresAt() {
			return ErrTokenInvalid
		}
		cur := refreshState{token: row.Token(), prev: row.PrevToken(), rotatedAt: row.RotatedAt()}
		next, err := rotateRefresh(cur, hash, token.Hash(out.refreshToken), now)
		if err != nil {
			return err
		}

		// token 通过之后才检查封禁，避免没有凭证的人探测出账号状态
		user := ptable.UserTable.GetById(ctx, uid)
		if user == nil {
			return fmt.Errorf("user %d not found", uid)
		}
		if user.Status() != 0 {
			return ErrAccountBanned
		}
		out.name = user.Name()
		out.requestToken = startSession(user, now)
		next.store(row, now)
		// 不在这里发送业务，而是等待客户端主动发进入大厅协议
		return nil
	})
	if err != nil {
		return err
	}

	if out.loginToken, err = redis.IssueLoginToken(ctx, uid, deviceID); err != nil {
		return err
	}
	return out.write(req.Session())
}

// findRefreshRow 事务外按哈希找免登录行：先当成当前 token 找，找不到再当成上一个 token 找
// （还在不在宽限期内，由锁内的 rotateRefresh 判断）。id 为 0 表示没有。
func findRefreshRow(ctx context.Context, hash string) (id, uid int64, err error) {
	err = dbpool.Query(ctx, func(ctx context.Context) error {
		row := ptable.UserRefreshTokenTable.SelectByToken(ctx, hash)
		if row == nil {
			if rows := ptable.UserRefreshTokenTable.SelectListByPrevToken(ctx, hash); len(rows) == 1 {
				row = rows[0]
			}
		}
		if row != nil {
			id, uid = row.Id(), row.Uid()
		}
		return nil
	})
	return id, uid, err
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

// findOrCreateAccount 找到或新建账号，结果写进 out。created 为 true 时新号已经在本函数的事务里
// 开好了会话（requestToken、免登录行都建好了）；为 false 时由调用方另开事务处理老号。
func findOrCreateAccount(ctx context.Context, platform int16, platUID, deviceID string, now time.Time, out *loginOutcome) (created bool, err error) {
	// 1. 先无锁查，绝大多数登录在这里就结束
	if out.uid, err = findUIDByIdentity(ctx, platform, platUID); err != nil || out.uid != 0 {
		return false, err
	}

	// 2. 没查到：按身份加命名锁，事务内再查一次，再建
	err = db.WithNamedLock(ctx, "identity", fmt.Sprintf("%d:%s", platform, platUID), func(ctx context.Context) error {
		if ident := ptable.UserIdentityTable.GetByPlatformPlatformUID(ctx, platform, platUID); ident != nil {
			out.uid = ident.Uid() // 排队期间别人已经建好了
			return nil
		}

		user := ptable.NewUser()
		user.SetCreatedAt(now.Unix())
		out.requestToken = startSession(user, now) // 和 user 同一行，Insert 直接带上
		ptable.UserTable.Insert(ctx, user)

		ident := ptable.NewUserIdentity()
		ident.SetUid(user.Id())
		ident.SetPlatform(platform)
		ident.SetPlatformUID(platUID)
		ident.SetCreatedAt(now.Unix())
		ptable.UserIdentityTable.Insert(ctx, ident)

		// 免登录行也要在这个事务里建，否则首次登录后没法免登录
		insertRefreshToken(ctx, user.Id(), deviceID, out.refreshToken, now)
		out.uid, out.name, created = user.Id(), user.Name(), true
		return nil
	})
	return created, err
}

func checkClientVersion(v string) error {
	// TODO: 过旧返回 ErrClientTooOld
	return nil
}

// ---- 免登录 token 表 ----
// token / prevToken 列里都是 token.Hash 之后的值，写入一律经过 refreshState.store。
// 调用方必须已持有 uid 锁；或者 uid 是刚建的新号（没有竞争）。

// refreshState 是一台设备的免登录凭证：当前 token、上一个 token（都是哈希）、上一个 token 被换下来的时间。
type refreshState struct {
	token     string
	prev      string
	rotatedAt int64
}

// newRefresh 新号或 SDK 登录：一份没有 prev 的新凭证。
func newRefresh(refreshToken string, now time.Time) refreshState {
	return refreshState{token: token.Hash(refreshToken), rotatedAt: now.Unix()}
}

// store 写回一行，绝对过期时间从 now 起重新算。
func (s refreshState) store(row *ptable.UserRefreshToken, now time.Time) {
	row.SetToken(s.token)
	row.SetPrevToken(s.prev)
	row.SetRotatedAt(s.rotatedAt)
	row.SetExpiresAt(now.Add(refreshTTL).Unix())
}

// rotateRefresh 校验客户端带来的 token，返回轮换后的状态（新 token 是 next）。presented、next 都是哈希：
//   - 带的是当前 token：正常轮换，当前 token 变成 prev，宽限期从现在开始；
//   - 带的是 prev 且还在宽限期内：上一次的响应丢了，再发一个新 token，prev 和宽限期都不变——
//     宽限期从 prev 被换下来那一刻起固定 refreshGrace，反复用 prev 不会续期；
//   - 其他情况一律 ErrTokenInvalid。
//
// 比的是哈希：就算从耗时里猜出库里的哈希，也反推不出 token，所以不用常数时间比较。
func rotateRefresh(cur refreshState, presented, next string, now time.Time) (refreshState, error) {
	switch {
	case presented == "":
		return cur, ErrTokenInvalid
	case presented == cur.token:
		return refreshState{token: next, prev: cur.token, rotatedAt: now.Unix()}, nil
	case cur.prev != "" && presented == cur.prev && now.Unix()-cur.rotatedAt <= int64(refreshGrace/time.Second):
		return refreshState{token: next, prev: cur.prev, rotatedAt: cur.rotatedAt}, nil
	}
	return cur, ErrTokenInvalid
}

func insertRefreshToken(ctx context.Context, uid int64, deviceID, refreshToken string, now time.Time) {
	row := ptable.NewUserRefreshToken()
	row.SetUid(uid)
	row.SetDeviceId(deviceID)
	newRefresh(refreshToken, now).store(row, now)
	ptable.UserRefreshTokenTable.Insert(ctx, row)
}

// upsertRefreshToken SDK 登录：整行换成新凭证，prev 和作废标记一起清掉
// （prev 不清的话，作废前的旧 token 在宽限期内还能用）。
// 没有 ON CONFLICT，所以先查后写；同一 (uid, device) 的写都在 uid 锁内，不会并发。
func upsertRefreshToken(ctx context.Context, uid int64, deviceID, refreshToken string, now time.Time) {
	row := ptable.UserRefreshTokenTable.GetByUidDeviceId(ctx, uid, deviceID)
	if row == nil {
		insertRefreshToken(ctx, uid, deviceID, refreshToken, now)
		return
	}
	newRefresh(refreshToken, now).store(row, now)
	row.SetRevokedAt(0)
}
