package mail

import (
	"errors"
	"testing"
)

// TestCheckClaimable 附件只能领一次，而且得真有附件。
func TestCheckClaimable(t *testing.T) {
	const award = `[{"id":1,"n":2}]`
	for _, tc := range []struct {
		state int32
		award string
		want  error
	}{
		{MailStateNotRead, award, nil},
		{MailStateReadNotReceived, award, nil},
		{0, award, nil}, // 改动前插入的未读数据
		{MailStateRead, award, ErrAttachmentClaimed},
		{MailStateNotRead, "[]", ErrNoAttachment},
		{MailStateRead, "", ErrNoAttachment},
	} {
		if err := checkClaimable(tc.state, tc.award); !errors.Is(err, tc.want) {
			t.Errorf("checkClaimable(%d, %q) = %v，want %v", tc.state, tc.award, err, tc.want)
		}
	}
}
