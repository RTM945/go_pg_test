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
	"net/http"
	"os"
	"strings"
	"time"

	"go_pg_test/app/test/msg"
	testpb "go_pg_test/genpb/test"
	gopgio "go_pg_test/io"
)

const (
	extToken int32 = 1 // Login.ext：免登录 token
	extUDID  int32 = 5 // Login.ext：设备 id

	testActId int32 = 42
)

type handler func(payload []byte) error

type Robot struct {
	addr     string
	account  string
	deviceID string
	client   *http.Client
	handlers map[int32]handler

	loginToken   string
	refreshToken string
	reqToken     string
	uid          int64
	name         string
	lastRed      *msg.SRedEnvelope
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

// Register 声明“哪个协议号由哪个函数处理”，和服务器侧 Register 是同一套思路。
func (r *Robot) Register[T gopgio.Msg](typeId int32, newMsg func() T, fn func(T) error) {
	r.handlers[typeId] = func(payload []byte) error {
		m := newMsg()
		if err := m.Read(payload); err != nil {
			return fmt.Errorf("decode typeId %d: %w", typeId, err)
		}
		return fn(m)
	}
}

func (r *Robot) registerHandlers() {
	r.Register(testpb.PROTOCOL_TYPE_LoginResult, msg.NewLoginResult, func(m *msg.LoginResult) error {
		r.uid = m.Uid
		r.name = m.Name
		r.refreshToken = m.RefreshToken
		log.Printf("← LoginResult uid=%d name=%q refreshToken=%s", m.Uid, m.Name, m.RefreshToken)
		return nil
	})

	r.Register(testpb.PROTOCOL_TYPE_SRedEnvelope, msg.NewSRedEnvelope, func(m *msg.SRedEnvelope) error {
		r.lastRed = m
		log.Printf("← SRedEnvelope actId=%d todayCount=%d total=%d", m.ActId, m.TodayCount, m.Total)
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
	payload := make([]byte, m.Size())
	n, err := m.Write(payload)
	if err != nil {
		return nil, fmt.Errorf("encode typeId %d: %w", m.TypeId(), err)
	}
	if n != len(payload) {
		return nil, fmt.Errorf("encode typeId %d: wrote %d/%d bytes", m.TypeId(), n, len(payload))
	}

	var frame bytes.Buffer
	if err := gopgio.WriteFrame(&frame, m.TypeId(), payload); err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, r.addr+"/", &frame)
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
	login := msg.NewLogin()
	login.Plat = "test"
	login.Account = r.account
	login.ClientVersion = "robot"
	login.Ext = map[int32]string{extUDID: r.deviceID}

	resp, err := r.send(login, "", "")
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

func (r *Robot) loginAuto() error {
	login := msg.NewLogin()
	login.Plat = "test"
	login.Account = r.account
	login.ClientVersion = "robot"
	login.Ext = map[int32]string{extUDID: r.deviceID, extToken: r.refreshToken}

	oldRefresh := r.refreshToken
	resp, err := r.send(login, "", "")
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

func (r *Robot) click(loginToken, reqToken string) (*response, error) {
	m := msg.NewCRedEnvelopeClick()
	m.ActId = testActId
	return r.send(m, loginToken, reqToken)
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

	step("2. 用户协议：带 loginToken + requestToken")
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

	step("3. 重放旧 requestToken，应该被拒绝")
	replay, err := r.click(login0, req0)
	if err != nil {
		return err
	}
	if replay.status == http.StatusOK {
		return fmt.Errorf("旧 requestToken 重放居然成功了")
	}
	if replay.errCode != "REQ_TOKEN_STALE" {
		return fmt.Errorf("重放返回 %d %s，want 401 REQ_TOKEN_STALE", replay.status, replay.errCode)
	}
	log.Printf("   拒绝: HTTP %d %s", replay.status, replay.errCode)

	step("4. 用轮换后的 requestToken 继续")
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

	step("5. 免登录：用 refreshToken 换一套新 token")
	r.refreshToken = refresh0
	if err := r.loginAuto(); err != nil {
		return err
	}
	login1, req3, refresh1 := r.loginToken, r.reqToken, r.refreshToken
	log.Printf("   新 loginToken=%s reqToken=%s refreshToken=%s", login1, req3, refresh1)

	step("6. 旧 loginToken 已被顶号")
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
	flag.Parse()

	if *account == "" {
		*account = fmt.Sprintf("robot-%d", time.Now().UnixNano())
	}

	log.Printf("robot account=%s device=%s addr=%s", *account, *deviceID, *addr)
	if err := NewRobot(*addr, *account, *deviceID).Run(); err != nil {
		log.Printf("robot failed: %v", err)
		os.Exit(1)
	}
}
