package db

import (
	"errors"
	"fmt"
	"testing"

	"go_pg_test/common/dispatch"
	"pdbgen/dbpool"
)

// TestClassify pdbgen 分得出的失败变成客户端能处理的错误码，原始错误还在链上（日志要用）；
// 其他错误原样返回。
func TestClassify(t *testing.T) {
	business := dispatch.NewClientError(dispatch.Conflict, "ALREADY", "already done")
	sqlErr := errors.New("relation does not exist")

	for _, tc := range []struct {
		name string
		err  error
		code string // 期望的 X-Error-Code；空表示不是 ClientError
	}{
		{"try lock", fmt.Errorf("%w: uid 1", dbpool.ErrAdvisoryLockNotAcquired), "REQ_IN_FLIGHT"},
		{"over budget", fmt.Errorf("%w: context deadline exceeded", dbpool.ErrTimeout), "BUSY"},
		{"commit unknown", fmt.Errorf("%w: conn closed", dbpool.ErrCommitUnknown), "BUSY"},
		{"business error", business, "ALREADY"},
		{"sql error", sqlErr, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.err)
			if !errors.Is(got, tc.err) {
				t.Fatalf("classify 丢了原始错误: %v", got)
			}
			ce, ok := errors.AsType[*dispatch.ClientError](got)
			switch {
			case tc.code == "" && ok:
				t.Fatalf("不该是 ClientError，got %s", ce.Code)
			case tc.code != "" && (!ok || ce.Code != tc.code):
				t.Fatalf("got %v，want code %s", got, tc.code)
			}
		})
	}

	if err := classify(nil); err != nil {
		t.Fatalf("classify(nil) = %v", err)
	}
}
