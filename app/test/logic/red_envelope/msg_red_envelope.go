package red_envelope

import (
	"context"

	"go_pg_test/app/test/session"
	testpb "go_pg_test/genpb/test"
)

func ProcessCRedEnvelope(ctx context.Context, s *session.User, req *testpb.CRedEnvelope) error {
	userRedEnvelope := Get(ctx, s.UID(), req.GetActId(), false)
	return userRedEnvelope.Online(s)
}

func ProcessCRedEnvelopeClick(ctx context.Context, s *session.User, req *testpb.CRedEnvelopeClick) error {
	userRedEnvelope := Get(ctx, s.UID(), req.GetActId(), false)
	return userRedEnvelope.Click(s)
}
