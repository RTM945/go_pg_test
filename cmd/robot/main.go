// robot 跑一遍登录 → 用户协议 → 重放拒绝 → 免登录 → 顶号的完整流程。
//
// 需要服务端已经起来（默认 127.0.0.1:8080）：
//
//	go run ./app/test -schema app/test/pdb.xml -addr :8080
//	go run ./cmd/robot
//
// 协议的响应处理用 Register 声明式挂载，新增协议只需要加一行 Register，
// 不用改分发逻辑。
package main

import (
	"bytes"
	"flag"
	"fmt"
	stdio "io"
	"log"
	"maps"
	"net/http"
	"os"
	"strings"
	"time"

	"go_pg_test/app/test/logic/event"
	testpb "go_pg_test/genpb/test"
	gopgio "go_pg_test/io"
)

const (
	extToken int32 = 1 // Login.ext：免登录 token
	extUDID  int32 = 5 // Login.ext：设备 id

	testActId int32 = 42
)

type handler func(payload []byte) error

type eventVersion struct {
	global int64
	user   int64
}

func (v eventVersion) String() string {
	return fmt.Sprintf("{global:%d user:%d}", v.global, v.user)
}

type Robot struct {
	addr     string
	account  string
	deviceID string
	client   *http.Client
	handlers map[int32]handler

	loginToken            string
	refreshToken          string
	reqToken              string
	uid                   int64
	name                  string
	lastRed               *testpb.SRedEnvelope
	eventVersions         map[int32]eventVersion
	eventVersionsReceived bool
	expectEvent           bool
	mailList              []*testpb.MailData
	attachmentKey         int64
	expectMail            bool
}

func NewRobot(addr, account, deviceID string) *Robot {
	r := &Robot{
		addr:     strings.TrimRight(addr, "/"),
		account:  account,
		deviceID: deviceID,
		client:   &http.Client{Timeout: 10 * time.Second},
		handlers: make(map[int32]handler),
	}
	r.registerHandlers()
	return r
}

// Register 声明“哪个协议由哪个函数处理”，和服务器侧 Handle 是同一套思路：
// 协议号取自 fn 参数的类型，调用方只写处理函数。
func (r *Robot) Register[T any, PT interface {
	*T
	gopgio.Msg
}](fn func(PT) error) {
	var zero PT
	typeId := zero.TypeId()
	r.handlers[typeId] = func(payload []byte) error {
		m := PT(new(T))
		if err := m.UnmarshalVT(payload); err != nil {
			return fmt.Errorf("decode typeId %d: %w", typeId, err)
		}
		return fn(m)
	}
}

func (r *Robot) registerHandlers() {
	r.Register(func(m *testpb.LoginResult) error {
		r.uid = m.Uid
		r.name = m.Name
		r.refreshToken = m.RefreshToken
		log.Printf("← LoginResult uid=%d name=%q refreshToken=%s", m.Uid, m.Name, m.RefreshToken)
		return nil
	})

	r.Register(func(m *testpb.SRedEnvelope) error {
		r.lastRed = m
		log.Printf("← SRedEnvelope actId=%d todayCount=%d total=%d", m.ActId, m.TodayCount, m.Total)
		return nil
	})

	r.Register(func(m *testpb.EventVersions) error {
		r.eventVersionsReceived = true
		r.eventVersions = make(map[int32]eventVersion, len(m.Versions))
		for typ, v := range m.Versions {
			if v == nil {
				continue
			}
			r.eventVersions[typ] = eventVersion{global: v.Global, user: v.User}
		}
		log.Printf("← EventVersions %v", r.eventVersions)
		return nil
	})

	r.Register(func(m *testpb.SMails) error {
		r.mailList = m.Mails
		log.Printf("← SMails count=%d", len(m.Mails))
		for _, mail := range m.Mails {
			log.Printf("   mail key=%d confId=%d state=%d mailTime=%d params=%s awards=%s",
				mail.Key, mail.ConfId, mail.State, mail.MailTime, mail.Params, mail.AwardList)
		}
		return nil
	})

	r.Register(func(m *testpb.SGetAttachment) error {
		r.attachmentKey = m.Key
		log.Printf("← SGetAttachment key=%d", m.Key)
		return nil
	})
}

type response struct {
	status     int
	errCode    string
	body       string
	loginToken string
	reqToken   string
}

func (r *Robot) send(m gopgio.Msg, loginToken, reqToken string) (*response, error) {
	frame, err := gopgio.AppendMsg(nil, m)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, r.addr+"/", bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/octet-stream")
	if loginToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+loginToken)
	}
	if reqToken != "" {
		httpReq.Header.Set("X-Request-Token", reqToken)
	}

	httpResp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post typeId %d: %w", m.TypeId(), err)
	}
	defer httpResp.Body.Close()

	resp := &response{
		status:     httpResp.StatusCode,
		errCode:    httpResp.Header.Get("X-Error-Code"),
		loginToken: httpResp.Header.Get("X-Login-Token"),
		reqToken:   httpResp.Header.Get("X-Request-Token"),
	}
	if httpResp.StatusCode != http.StatusOK {
		body, _ := stdio.ReadAll(httpResp.Body)
		resp.body = strings.TrimSpace(string(body))
		return resp, nil
	}

	frames, err := gopgio.ReadFrames(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response frames: %w", err)
	}
	for _, f := range frames {
		h, ok := r.handlers[f.TypeId]
		if !ok {
			return nil, fmt.Errorf("no handler registered for response typeId %d", f.TypeId)
		}
		if err := h(f.Payload); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (r *Robot) loginSDK() error {
	resp, err := r.sendLogin(r.account, map[int32]string{extUDID: r.deviceID})
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return fmt.Errorf("login failed: HTTP %d %s: %s", resp.status, resp.errCode, resp.body)
	}
	if r.refreshToken == "" {
		return fmt.Errorf("login response missing refreshToken")
	}
	if resp.loginToken == "" || resp.reqToken == "" {
		return fmt.Errorf("login response missing token headers: login=%q req=%q", resp.loginToken, resp.reqToken)
	}
	r.loginToken, r.reqToken = resp.loginToken, resp.reqToken
	return nil
}

// loginAuto 免登录只带 refreshToken 和设备号，不带 account：服务端只凭 refreshToken 找人。
func (r *Robot) loginAuto() error {
	oldRefresh := r.refreshToken
	resp, err := r.sendLogin("", map[int32]string{extUDID: r.deviceID, extToken: r.refreshToken})
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return fmt.Errorf("auto login failed: HTTP %d %s: %s", resp.status, resp.errCode, resp.body)
	}
	if resp.loginToken == "" || resp.reqToken == "" {
		return fmt.Errorf("auto login response missing token headers")
	}
	if r.refreshToken == "" || r.refreshToken == oldRefresh {
		return fmt.Errorf("auto login did not rotate refreshToken")
	}
	r.loginToken, r.reqToken = resp.loginToken, resp.reqToken
	return nil
}

// sendLogin 发一个登录，原样返回响应（不管成败）。
// 带 account 是 SDK 登录，ext 里带 extToken 是免登录，服务端要求恰好带一个。
func (r *Robot) sendLogin(account string, ext map[int32]string) (*response, error) {
	login := &testpb.CLogin{Plat: "test", Account: account, ClientVersion: "robot", Ext: ext}
	return r.send(login, "", "")
}

// expectLoginRejected 发一个应该被拒绝的登录，检查状态码和错误码。
func (r *Robot) expectLoginRejected(account string, ext map[int32]string, status int, code string) error {
	resp, err := r.sendLogin(account, ext)
	if err != nil {
		return err
	}
	if resp.status != status || resp.errCode != code {
		return fmt.Errorf("登录返回 %d %s，want %d %s", resp.status, resp.errCode, status, code)
	}
	log.Printf("   拒绝: HTTP %d %s", resp.status, resp.errCode)
	return nil
}

func (r *Robot) click(loginToken, reqToken string) (*response, error) {
	return r.send(&testpb.CRedEnvelopeClick{ActId: testActId}, loginToken, reqToken)
}

func (r *Robot) heartbeat() (*response, error) {
	return r.send(&testpb.CHeartbeat{}, r.loginToken, r.reqToken)
}

func (r *Robot) pollEvents() (*response, error) {
	return r.send(&testpb.CPollEvents{}, r.loginToken, r.reqToken)
}

func step(name string) {
	log.Printf("== %s", name)
}

func (r *Robot) Run() error {
	step("1. SDK 登录")
	if err := r.loginSDK(); err != nil {
		return err
	}
	login0, req0, refresh0 := r.loginToken, r.reqToken, r.refreshToken
	log.Printf("   loginToken=%s reqToken=%s refreshToken=%s", login0, req0, refresh0)

	step("2. 心跳：auth-only，不轮换 requestToken")
	reqBeforeHeartbeat := r.reqToken
	hbResp, err := r.heartbeat()
	if err != nil {
		return err
	}
	if hbResp.status != http.StatusOK {
		return fmt.Errorf("heartbeat failed: HTTP %d %s: %s", hbResp.status, hbResp.errCode, hbResp.body)
	}
	if hbResp.reqToken != "" {
		return fmt.Errorf("心跳不应下发新 requestToken，got %q", hbResp.reqToken)
	}
	if r.reqToken != reqBeforeHeartbeat {
		return fmt.Errorf("心跳改动了 requestToken: %q -> %q", reqBeforeHeartbeat, r.reqToken)
	}
	log.Printf("   心跳 ok，requestToken 未变: %s", r.reqToken)

	step("3. CPollEvents：按事件类型比较版本号")
	reqBeforePoll := r.reqToken
	beforeVersions := maps.Clone(r.eventVersions)
	pollResp, err := r.pollEvents()
	if err != nil {
		return err
	}
	if pollResp.status != http.StatusOK {
		return fmt.Errorf("poll events failed: HTTP %d %s: %s", pollResp.status, pollResp.errCode, pollResp.body)
	}
	if pollResp.reqToken != "" {
		return fmt.Errorf("CPollEvents 不应下发新 requestToken，got %q", pollResp.reqToken)
	}
	if r.reqToken != reqBeforePoll {
		return fmt.Errorf("CPollEvents 改动了 requestToken: %q -> %q", reqBeforePoll, r.reqToken)
	}
	if !r.eventVersionsReceived {
		return fmt.Errorf("CPollEvents 没有返回 EventVersions")
	}
	afterVersions := r.eventVersions
	mailBefore, mailAfter := beforeVersions[event.EventTypeMailNew], afterVersions[event.EventTypeMailNew]
	// 全服事件看 global，定向事件看 user；任一增加都说明 mail 需要同步。
	if r.expectEvent && mailAfter.global <= mailBefore.global && mailAfter.user <= mailBefore.user {
		return fmt.Errorf("期望 mail 版本增加，before=%v after=%v", mailBefore, mailAfter)
	}
	log.Printf("   版本: %v（mail %v -> %v）", afterVersions, mailBefore, mailAfter)

	// 再拉一次：没有新的变化，版本应该完全一致。
	secondBefore := maps.Clone(r.eventVersions)
	dupResp, err := r.pollEvents()
	if err != nil {
		return err
	}
	if dupResp.status != http.StatusOK {
		return fmt.Errorf("second poll failed: HTTP %d %s: %s", dupResp.status, dupResp.errCode, dupResp.body)
	}
	// 共享环境里两次 poll 之间可能真的有新事件，不能要求 map 完全相等；
	// 只要求版本不回退（回退说明服务端重置，需要全量同步）。
	for typ, before := range secondBefore {
		after := r.eventVersions[typ]
		if after.global < before.global || after.user < before.user {
			return fmt.Errorf("第二次拉取版本回退: type=%d before=%v after=%v", typ, before, after)
		}
	}
	log.Printf("   第二次拉取版本未回退: %v -> %v", secondBefore, r.eventVersions)

	step("4. 用户协议：带 loginToken + requestToken")
	resp, err := r.click(login0, req0)
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return fmt.Errorf("business request failed: HTTP %d %s: %s", resp.status, resp.errCode, resp.body)
	}
	if resp.reqToken == "" || resp.reqToken == req0 {
		return fmt.Errorf("requestToken 没有轮换: old=%q new=%q", req0, resp.reqToken)
	}
	req1 := resp.reqToken
	log.Printf("   requestToken 轮换: %s -> %s", req0, req1)

	step("5. 重放旧 requestToken，应该被拒绝")
	replay, err := r.click(login0, req0)
	if err != nil {
		return err
	}
	if replay.status == http.StatusOK {
		return fmt.Errorf("旧 requestToken 重放居然成功了")
	}
	if replay.errCode != "REQ_DUP" {
		return fmt.Errorf("重放返回 %d %s，want 409 REQ_DUP", replay.status, replay.errCode)
	}
	log.Printf("   拒绝: HTTP %d %s（Redis 快速去重）", replay.status, replay.errCode)

	step("6. 用轮换后的 requestToken 继续")
	resp, err = r.click(login0, req1)
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return fmt.Errorf("second business request failed: HTTP %d %s: %s", resp.status, resp.errCode, resp.body)
	}
	if resp.reqToken == "" || resp.reqToken == req1 {
		return fmt.Errorf("requestToken 第二次没有轮换: old=%q new=%q", req1, resp.reqToken)
	}
	req2 := resp.reqToken
	log.Printf("   requestToken 再次轮换: %s -> %s", req1, req2)

	step("7. CMails：拉取邮件并领取第一封")
	req3 := req2
	syncResp, err := r.send(&testpb.CMails{}, login0, req3)
	if err != nil {
		return err
	}
	if syncResp.status != http.StatusOK {
		return fmt.Errorf("sync mail failed: HTTP %d %s: %s", syncResp.status, syncResp.errCode, syncResp.body)
	}
	if syncResp.reqToken == "" || syncResp.reqToken == req3 {
		return fmt.Errorf("CMails 没有轮换 requestToken: old=%q new=%q", req3, syncResp.reqToken)
	}
	log.Printf("   CMails 轮换: %s -> %s，邮件数 %d", req3, syncResp.reqToken, len(r.mailList))
	req3 = syncResp.reqToken
	if r.expectMail && len(r.mailList) == 0 {
		return fmt.Errorf("期望有邮件，实际 SMails 为空")
	}

	if len(r.mailList) > 0 {
		first := r.mailList[0]

		// CReadMail 没有响应包，只能靠下一次同步确认状态。
		readResp, err := r.send(&testpb.CReadMail{Key: first.Key}, login0, req3)
		if err != nil {
			return err
		}
		if readResp.status != http.StatusOK {
			return fmt.Errorf("read mail failed: HTTP %d %s: %s", readResp.status, readResp.errCode, readResp.body)
		}
		if readResp.reqToken == "" || readResp.reqToken == req3 {
			return fmt.Errorf("CReadMail 没有轮换 requestToken: old=%q new=%q", req3, readResp.reqToken)
		}
		req3 = readResp.reqToken

		syncResp, err = r.send(&testpb.CMails{}, login0, req3)
		if err != nil {
			return err
		}
		if syncResp.status != http.StatusOK {
			return fmt.Errorf("sync after read failed: HTTP %d %s: %s", syncResp.status, syncResp.errCode, syncResp.body)
		}
		req3 = syncResp.reqToken
		stateAfterRead := int32(0)
		for _, mail := range r.mailList {
			if mail.Key == first.Key {
				stateAfterRead = mail.State
				break
			}
		}
		// 有附件是 3，无附件（或之前已领取）是 1。
		if stateAfterRead != 1 && stateAfterRead != 3 {
			return fmt.Errorf("读取后 mail %d 状态是 %d，want 1/3", first.Key, stateAfterRead)
		}

		claim := &testpb.CGetAttachment{Key: first.Key}
		claimResp, err := r.send(claim, login0, req3)
		if err != nil {
			return err
		}
		if stateAfterRead == 1 {
			// 没有附件（或者之前已经领过）：领取被拒绝，事务回滚，requestToken 不轮换
			if claimResp.status != http.StatusConflict || (claimResp.errCode != "NO_ATTACHMENT" && claimResp.errCode != "ATTACHMENT_CLAIMED") {
				return fmt.Errorf("没有可领附件的邮件领取返回 %d %s，want 409 NO_ATTACHMENT / ATTACHMENT_CLAIMED", claimResp.status, claimResp.errCode)
			}
			log.Printf("   没有可领的附件，拒绝: HTTP %d %s", claimResp.status, claimResp.errCode)
		} else {
			if claimResp.status != http.StatusOK {
				return fmt.Errorf("get attachment failed: HTTP %d %s: %s", claimResp.status, claimResp.errCode, claimResp.body)
			}
			if claimResp.reqToken == "" || claimResp.reqToken == req3 {
				return fmt.Errorf("CGetAttachment 没有轮换 requestToken: old=%q new=%q", req3, claimResp.reqToken)
			}
			req3 = claimResp.reqToken
			if r.attachmentKey != first.Key {
				return fmt.Errorf("领取结果 key=%d，want %d", r.attachmentKey, first.Key)
			}

			// 附件只能领一次
			again, err := r.send(claim, login0, req3)
			if err != nil {
				return err
			}
			if again.status != http.StatusConflict || again.errCode != "ATTACHMENT_CLAIMED" {
				return fmt.Errorf("重复领取返回 %d %s，want 409 ATTACHMENT_CLAIMED", again.status, again.errCode)
			}
			log.Printf("   重复领取，拒绝: HTTP %d %s", again.status, again.errCode)

			// 再同步一次，确认领取状态已经落库。
			syncResp, err = r.send(&testpb.CMails{}, login0, req3)
			if err != nil {
				return err
			}
			if syncResp.status != http.StatusOK {
				return fmt.Errorf("re-sync mail failed: HTTP %d %s: %s", syncResp.status, syncResp.errCode, syncResp.body)
			}
			req3 = syncResp.reqToken
			found := false
			for _, mail := range r.mailList {
				if mail.Key == first.Key && mail.State == 1 {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("重新同步后 mail %d 不是已领取状态", first.Key)
			}
		}
	}

	step("8. 免登录：只带 refreshToken，换一套新 token")
	r.refreshToken = refresh0
	if err := r.loginAuto(); err != nil {
		return err
	}
	login1, req3, refresh1 := r.loginToken, r.reqToken, r.refreshToken
	log.Printf("   新 loginToken=%s reqToken=%s refreshToken=%s", login1, req3, refresh1)

	step("9. 上一次的响应丢了：宽限期内用旧 refreshToken 还能免登录")
	r.refreshToken = refresh0
	if err := r.loginAuto(); err != nil {
		return err
	}
	log.Printf("   旧 refreshToken 在宽限期内可用，换了一套新的 loginToken=%s refreshToken=%s", r.loginToken, r.refreshToken)

	step("10. 伪造的 refreshToken：401 TOKEN_INVALID，客户端回到 SDK 登录")
	if err := r.expectLoginRejected("", map[int32]string{extUDID: r.deviceID, extToken: "forged-" + refresh1},
		http.StatusUnauthorized, "TOKEN_INVALID"); err != nil {
		return err
	}

	step("11. 换一台设备拿当前的 refreshToken：401 TOKEN_INVALID，token 只属于拿到它的那台设备")
	if err := r.expectLoginRejected("", map[int32]string{extUDID: "other-" + r.deviceID, extToken: r.refreshToken},
		http.StatusUnauthorized, "TOKEN_INVALID"); err != nil {
		return err
	}

	step("12. 登录参数不合法：400 BAD_PAYLOAD")
	if err := r.expectLoginRejected(r.account, map[int32]string{extUDID: strings.Repeat("d", 200)},
		http.StatusBadRequest, "BAD_PAYLOAD"); err != nil {
		return fmt.Errorf("设备号超长: %w", err)
	}
	if err := r.expectLoginRejected(r.account, map[int32]string{extUDID: r.deviceID, extToken: r.refreshToken},
		http.StatusBadRequest, "BAD_PAYLOAD"); err != nil {
		return fmt.Errorf("account 和 refreshToken 都带: %w", err)
	}

	step("13. 旧 loginToken 已被顶号")
	kicked, err := r.click(login0, req3)
	if err != nil {
		return err
	}
	if kicked.status != http.StatusUnauthorized || kicked.errCode != "KICKED" {
		return fmt.Errorf("旧 loginToken 返回 %d %s，want 401 KICKED", kicked.status, kicked.errCode)
	}
	log.Printf("   拒绝: HTTP %d %s", kicked.status, kicked.errCode)

	step("robot ok")
	return nil
}

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8080", "server address")
	account := flag.String("account", "", "test account; empty = random")
	deviceID := flag.String("device", "robot-device", "device id")
	expectEvent := flag.Bool("expect-event", false, "require at least one event in the first CPollEvents")
	expectMail := flag.Bool("expect-mail", false, "require at least one mail in CMails")
	flag.Parse()

	if *account == "" {
		*account = fmt.Sprintf("robot-%d", time.Now().UnixNano())
	}

	log.Printf("robot account=%s device=%s addr=%s", *account, *deviceID, *addr)
	robot := NewRobot(*addr, *account, *deviceID)
	robot.expectEvent = *expectEvent
	robot.expectMail = *expectMail
	if err := robot.Run(); err != nil {
		log.Printf("robot failed: %v", err)
		os.Exit(1)
	}
}
