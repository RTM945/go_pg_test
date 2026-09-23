package main

import (
	"context"
	"go_pg_test/logic/redenvelope"
	"pdbgen/dbpool"
	"pdbgen/readxml"
)

func main() {
	schema, err := readxml.LoadSchema("./pdb.xml")
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	err = dbpool.Init(ctx, schema)
	if err != nil {
		panic(err)
	}
	var uid int64 = 1001
	lockKey := "user_red_envelope"
	var processUserRedEnvelopeOnline = func(ctx context.Context) error {
		userRedEnvelope := redenvelope.Get(ctx, uid, 1, false)
		userRedEnvelope.Online()
		return nil
	}
	err = dbpool.WithTryAdvisoryLock(ctx, lockKey, uid, processUserRedEnvelopeOnline)
	if err != nil {
		panic(err)
	}
}
