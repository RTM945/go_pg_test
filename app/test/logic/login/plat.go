package login

import "context"

type PlatformVerifier interface {
	PlatformID() int16
	// Verify 返回平台侧的稳定用户标识（openid 等）。必须在事务外调用，要自己设超时。
	Verify(ctx context.Context, account string, ext map[int32]string) (platformUID string, err error)
}

var verifiers = map[string]PlatformVerifier{} // key 是 CLogin.Plat，启动时注册

type TestPlatform struct {
}

func (*TestPlatform) PlatformID() int16 {
	return 0
}

func (*TestPlatform) Verify(ctx context.Context, account string, ext map[int32]string) (platformUID string, err error) {
	if account == "" {
		return "", ErrLoginFailed
	}
	return account, nil
}

func init() {
	verifiers["test"] = &TestPlatform{}
}
