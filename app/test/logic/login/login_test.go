package login

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go_pg_test/common/dispatch"
)

// TestRotateRefresh 当前 token 正常轮换；上一个 token 只在被换下来后的 refreshGrace 内可用，
// 反复使用也不续期；其他一律 TOKEN_INVALID。
func TestRotateRefresh(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	cur := refreshState{token: "B", prev: "A", rotatedAt: t0.Unix()}

	later := t0.Add(time.Hour)
	if next, err := rotateRefresh(cur, "B", "C", later); err != nil || next != (refreshState{"C", "B", later.Unix()}) {
		t.Fatalf("当前 token 应该正常轮换，实际 %+v %v", next, err)
	}

	next, err := rotateRefresh(cur, "A", "C", t0.Add(30*time.Second))
	if err != nil || next != (refreshState{"C", "A", t0.Unix()}) {
		t.Fatalf("宽限期内的旧 token 应该可用，且 prev 和宽限期都不变，实际 %+v %v", next, err)
	}
	// 距 prev 被换下来 70 秒：上一次重试没有把宽限期续上
	if _, err := rotateRefresh(next, "A", "D", t0.Add(70*time.Second)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("反复使用旧 token 不应该续期，实际 %v", err)
	}

	for _, presented := range []string{"X", ""} {
		if _, err := rotateRefresh(cur, presented, "C", t0); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("token %q 应该被拒绝，实际 %v", presented, err)
		}
	}
}

// TestCheckLoginParams plat 和设备号必填；account（SDK 登录）和 refreshToken（免登录）恰好带一个；
// 所有字段都不能超过 maxFieldLen。
func TestCheckLoginParams(t *testing.T) {
	long := strings.Repeat("x", maxFieldLen+1)
	for _, tc := range []struct {
		plat, account, device, refresh, version string
		ok                                      bool
	}{
		{"test", "a", "d", "", "1", true},   // SDK 登录
		{"test", "", "d", "r", "1", true},   // 免登录
		{"test", "a", "d", "r", "1", false}, // 两个都带
		{"test", "", "d", "", "1", false},   // 两个都不带
		{"", "a", "d", "", "1", false},
		{"test", "a", "", "", "1", false},
		{long, "a", "d", "", "1", false},
		{"test", long, "d", "", "1", false},
		{"test", "a", long, "", "1", false},
		{"test", "", "d", long, "1", false},
		{"test", "a", "d", "", long, false},
	} {
		err := checkLoginParams(tc.plat, tc.account, tc.device, tc.refresh, tc.version)
		if (err == nil) != tc.ok || err != nil && !errors.Is(err, dispatch.ErrMalformedPayload) {
			t.Errorf("checkLoginParams(%q, %q, %q, %q, %q) = %v", tc.plat, tc.account, tc.device, tc.refresh, tc.version, err)
		}
	}
}
