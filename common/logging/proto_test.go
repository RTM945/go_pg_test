package logging

import (
	"regexp"
	"strings"
	"testing"

	testpb "go_pg_test/genpb/test"
)

// TestProtoValueRedactsSensitiveFields 打了 (proto.sensitive) 的字段只打成 "***"，原消息不变。
func TestProtoValueRedactsSensitiveFields(t *testing.T) {
	login := &testpb.Login{Plat: "test", Account: "acc", Ext: map[int32]string{1: "refresh-secret", 5: "device-1"}}
	got := ProtoValue(login).String()
	if strings.Contains(got, "refresh-secret") || strings.Contains(got, "device-1") ||
		!strings.Contains(got, `"***"`) || !strings.Contains(got, `"acc"`) {
		t.Fatalf("Login 的 ext 应该只隐去 value、其他字段照常，实际: %s", got)
	}
	if login.Ext[1] != "refresh-secret" {
		t.Fatal("ProtoValue 不能改原消息")
	}

	res := &testpb.LoginResult{Uid: 7, RefreshToken: "rt-secret"}
	if got := ProtoValue(res).String(); strings.Contains(got, "rt-secret") || !regexp.MustCompile(`uid:\s*7`).MatchString(got) {
		t.Fatalf("LoginResult 的 refreshToken 应该隐去，实际: %s", got)
	}
}

// TestProtoValueKeepsOrdinaryMessages 没有 sensitive 字段的消息原样打印。
func TestProtoValueKeepsOrdinaryMessages(t *testing.T) {
	m := &testpb.SRedEnvelope{ActId: 3, TodayCount: 2}
	if got := ProtoValue(m).String(); !regexp.MustCompile(`actId:\s*3`).MatchString(got) || strings.Contains(got, "***") {
		t.Fatalf("普通消息应该原样打印，实际: %s", got)
	}
}
