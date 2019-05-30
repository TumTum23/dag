package dsync

import (
	"context"
	"fmt"
	"io"
	"math/rand"

	ipld "github.com/ipfs/go-ipld-format"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/qri-io/dag"
)

// session tracks the state of a transfer
type session struct {
	sid    string
	ctx    context.Context
	lng    ipld.NodeGetter
	bapi   coreiface.BlockAPI
	pin    bool
	mfst   *dag.Manifest
	diff   *dag.Manifest
	prog   dag.Completion
	progCh chan dag.Completion
}

// newSession creates a receive state machine
func newSession(ctx context.Context, lng ipld.NodeGetter, bapi coreiface.BlockAPI, mfst *dag.Manifest, pinOnComplete bool) (*session, error) {
	// TODO (b5): ipfs api/v0/get/block doesn't allow checking for local blocks yet
	// aren't working over ipfs api, so we can't do delta's quite yet. Just send the whole things back
	diff := mfst

	// diff, err := dag.Missing(ctx, lng, mfst)
	// if err != nil {
	// 	return nil, err
	// }

	s := &session{
		sid:    randStringBytesMask(10),
		ctx:    ctx,
		lng:    lng,
		bapi:   bapi,
		mfst:   mfst,
		diff:   diff,
		pin:    pinOnComplete,
		prog:   dag.NewCompletion(mfst, diff),
		progCh: make(chan dag.Completion),
	}

	go s.completionChanged()

	return s, nil
}

// ReceiveBlock accepts a block from the sender, placing it in the local blockstore
func (s *session) ReceiveBlock(hash string, data io.Reader) ReceiveResponse {
	bstat, err := s.bapi.Put(s.ctx, data)

	if err != nil {
		return ReceiveResponse{
			Hash:   hash,
			Status: StatusRetry,
			Err:    err,
		}
	}

	id := bstat.Path().Cid()
	if id.String() != hash {
		return ReceiveResponse{
			Hash:   hash,
			Status: StatusErrored,
			Err:    fmt.Errorf("hash mismatch. expected: '%s', got: '%s'", hash, id.String()),
		}
	}

	// this should be the only place that modifies progress
	for i, h := range s.mfst.Nodes {
		if hash == h {
			s.prog[i] = 100
		}
	}
	go s.completionChanged()

	return ReceiveResponse{
		Hash:   hash,
		Status: StatusOk,
	}
}

// Complete returns if this receive session is finished or not
func (s *session) Complete() bool {
	return s.prog.Complete()
}

func (s *session) completionChanged() {
	s.progCh <- s.prog
}

// the best stack overflow answer evaarrr: https://stackoverflow.com/a/22892986/9416066
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const (
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
)

func randStringBytesMask(n int) string {
	b := make([]byte, n)
	// A rand.Int63() generates 63 random bits, enough for letterIdxMax letters!
	for i, cache, remain := n-1, rand.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = rand.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			b[i] = letterBytes[idx]
			i--
		}
		cache >>= letterIdxBits
		remain--
	}

	return string(b)
}
