// gmmail 模拟 GM 平台：把同一条 GM 邮件广播给所有游戏服，等所有服都
// 幂等写库并刷新缓存后，再通知一次客户端事件。
//
//	go run ./cmd/gmmail -token dev \
//	  -servers http://127.0.0.1:8080,http://127.0.0.1:8081 \
//	  -params '{"title":"维护补偿"}' -awards '[...]'
//
// 服务端要带 -gm-token dev 启动；两个 endpoints：
//
//	POST /gm/mail         每个服幂等写 gm_mail + reload 本地缓存
//	POST /gm/mail/notify  确认所有服成功后 bump 全局邮件事件
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go_pg_test/common/token"
)

const hdrGMToken = "X-GM-Token"

type mailRequest struct {
	UUID          string `json:"uuid"`
	Params        string `json:"params"`
	AwardList     string `json:"awardList"`
	StartAt       int64  `json:"startAt"`
	EndAt         int64  `json:"endAt"`
	Channel       string `json:"channel"`
	UserCreatedAt int64  `json:"userCreatedAt"`
	Condition     string `json:"condition"`
}

type mailResponse struct {
	ID      int64 `json:"id"`
	Created bool  `json:"created"`
}

type notifyResponse struct {
	EventSeq int64 `json:"eventSeq"`
}

func main() {
	servers := flag.String("servers", "http://127.0.0.1:8080", "comma-separated game server addresses")
	gmToken := flag.String("token", "", "X-GM-Token configured on game servers")
	uuid := flag.String("uuid", "", "idempotency key; empty = generate")
	params := flag.String("params", `{"title":"test mail","content":"hello"}`, "mail params json")
	awards := flag.String("awards", "[]", "award list json")
	startAt := flag.Int64("start-at", 0, "unix seconds; 0 = no lower bound")
	endAt := flag.Int64("end-at", 0, "unix seconds; 0 = no upper bound")
	userCreatedAt := flag.Int64("user-created-at", 0, "only users created before this unix time; 0 = all")
	channel := flag.String("channel", "", "target channel; empty = all")
	condition := flag.String("condition", "", "reserved script condition")
	flag.Parse()

	if *gmToken == "" {
		fail("-token is required")
	}
	if *uuid == "" {
		*uuid = token.New32()
	}

	var urls []string
	for _, s := range strings.Split(*servers, ",") {
		s = strings.TrimRight(strings.TrimSpace(s), "/")
		if s != "" {
			urls = append(urls, s)
		}
	}
	if len(urls) == 0 {
		fail("no servers")
	}

	payload := mailRequest{
		UUID:          *uuid,
		Params:        *params,
		AwardList:     *awards,
		StartAt:       *startAt,
		EndAt:         *endAt,
		Channel:       *channel,
		UserCreatedAt: *userCreatedAt,
		Condition:     *condition,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		fail("marshal: %v", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	ctx := context.Background()
	for _, server := range urls {
		var resp mailResponse
		if err := postJSON(ctx, client, server+"/gm/mail", *gmToken, body, &resp); err != nil {
			fail("%s: %v", server, err)
		}
		fmt.Printf("server=%s mailID=%d created=%v\n", server, resp.ID, resp.Created)
	}

	// 所有服都确认写完并刷新缓存后，才让客户端开始同步，避免某个服
	// 先收到事件但缓存里还没有这封邮件。
	var notify notifyResponse
	if err := postJSON(ctx, client, urls[0]+"/gm/mail/notify", *gmToken, nil, &notify); err != nil {
		fail("%s notify: %v", urls[0], err)
	}
	fmt.Printf("uuid=%s eventSeq=%d\n", *uuid, notify.EventSeq)
}

func postJSON(ctx context.Context, client *http.Client, url, gmToken string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(hdrGMToken, gmToken)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
