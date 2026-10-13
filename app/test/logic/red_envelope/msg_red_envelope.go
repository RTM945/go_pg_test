package red_envelope

import (
	"context"
	"go_pg_test/app/test/msg"
)

func ProcessCRedEnvelope(ctx context.Context, req *msg.CRedEnvelope) error {
	uid := req.Session().UID
	userRedEnvelope := Get(ctx, uid, req.GetActId(), false)
	return userRedEnvelope.Online(req.Session())
}

func ProcessCRedEnvelopeClick(ctx context.Context, req *msg.CRedEnvelopeClick) error {
	uid := req.Session().UID
	userRedEnvelope := Get(ctx, uid, req.GetActId(), false)
	return userRedEnvelope.Click(req.Session())
}
