package dsync

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/qri-io/dag"
)

func TestSyncHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, a, err := makeAPI(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, b, err := makeAPI(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// yooooooooooooooooooooo...
	f := files.NewReaderFile(ioutil.NopCloser(strings.NewReader("y" + strings.Repeat("o", 3500000))))
	path, err := a.Unixfs().Add(ctx, f)
	if err != nil {
		t.Fatal(err)
	}

	aGetter := &dag.NodeGetter{Dag: a.Dag()}
	info, err := dag.NewInfo(ctx, aGetter, path.Cid())
	if err != nil {
		t.Fatal(err)
	}

	onCompleteCalled := make(chan struct{}, 1)
	onCompleteHook := func(_ context.Context, _ dag.Info, _ map[string]string) error {
		onCompleteCalled <- struct{}{}
		return nil
	}

	removeCheckCalled := make(chan struct{}, 1)
	removeCheckHook := func(_ context.Context, _ dag.Info, _ map[string]string) error {
		removeCheckCalled <- struct{}{}
		return nil
	}

	bGetter := &dag.NodeGetter{Dag: b.Dag()}
	bdsync, err := New(bGetter, b.Block(), func(cfg *Config) {
		cfg.AllowRemoves = true
		cfg.PushPreCheck = func(context.Context, dag.Info, map[string]string) error { return nil }
		cfg.PushComplete = onCompleteHook
		cfg.RemoveCheck = removeCheckHook
	})
	if err != nil {
		t.Fatal(err)
	}

	s := httptest.NewServer(HTTPRemoteHandler(bdsync))
	defer s.Close()

	cli := &HTTPClient{URL: s.URL + "/dsync"}

	t.Logf("pushing %#v\n", info.Manifest.Nodes)
	push, err := NewPush(aGetter, info, cli, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := push.Do(ctx); err != nil {
		t.Fatal(err)
	}

	// b should now be able to generate a manifest
	_, err = dag.NewManifest(ctx, bGetter, path.Cid())
	if err != nil {
		t.Error(err)
	}

	<-onCompleteCalled

	if err := cli.RemoveCID(ctx, info.RootCID().String(), nil); err != nil {
		t.Fatal(err)
	}

	<-removeCheckCalled

	r, err := cli.OpenBlockStream(ctx, info, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ioutil.ReadAll(r); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveNotSupported(t *testing.T) {
	ctx := context.Background()

	_, b, err := makeAPI(ctx)
	if err != nil {
		t.Fatal(err)
	}

	bGetter := &dag.NodeGetter{Dag: b.Dag()}
	bdsync, err := New(bGetter, b.Block())
	if err != nil {
		t.Fatal(err)
	}

	s := httptest.NewServer(HTTPRemoteHandler(bdsync))
	defer s.Close()

	cli := &HTTPClient{URL: s.URL + "/dsync"}
	if err := cli.RemoveCID(ctx, "foo", nil); err != ErrRemoveNotSupported {
		t.Errorf("expected error remoce not supported, got: %s", err.Error())
	}
}

func TestHooksMetaHTTP(t *testing.T) {
	ctx, done := context.WithCancel(context.Background())
	defer done()

	nodeA, nodeB := mustNewLocalRemoteIPFSAPI(ctx)
	cid := mustAddOneBlockDAG(nodeA)
	ang, err := NewLocalNodeGetter(nodeA)
	if err != nil {
		t.Fatal(err)
	}
	aDsync, err := New(ang, nodeA.Block())
	if err != nil {
		t.Fatal(err)
	}

	bng, err := NewLocalNodeGetter(nodeB)
	if err != nil {
		t.Fatal(err)
	}

	check := map[string]string{
		"hello":   "world",
		"this is": "fun",
	}

	checkMeta := func(hookName string) Hook {
		return func(_ context.Context, _ dag.Info, meta map[string]string) error {
			if diff := cmp.Diff(check, meta); diff != "" {
				t.Errorf("%s hook response mismatch (-want +got):\n%s", hookName, diff)
			}
			return nil
		}
	}

	bAddr := ":9595"
	remoteAddr := fmt.Sprintf("http://localhost%s/dsync", bAddr)

	bDsync, err := New(bng, nodeB.Block(), func(cfg *Config) {
		cfg.HTTPRemoteAddress = bAddr
		cfg.AllowRemoves = true
		cfg.PinAPI = nodeB.Pin()

		cfg.PushPreCheck = checkMeta("PushPreCheck")
		cfg.PushFinalCheck = checkMeta("PushFinalCheck")
		cfg.PushComplete = checkMeta("PushComplete")
		cfg.GetDagInfoCheck = checkMeta("GetDagInfoCheck")
		cfg.OpenBlockStreamCheck = checkMeta("OpenBlockStreamCheck")
		cfg.RemoveCheck = checkMeta("RemoveCheck")
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = bDsync.StartRemote(ctx); err != nil {
		t.Fatal(err)
	}

	push, err := aDsync.NewPush(cid.String(), remoteAddr, true)
	push.SetMeta(check)
	if err != nil {
		t.Fatal(err)
	}
	if err := push.Do(ctx); err != nil {
		t.Fatal(err)
	}

	pull, err := aDsync.NewPull(cid.String(), remoteAddr, check)
	if err != nil {
		t.Fatal(err)
	}

	if err := pull.Do(ctx); err != nil {
		t.Fatal(err)
	}

	// TODO (b5) - run a delete
}

func TestBackwardCompatibleClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, a, err := makeAPI(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, b, err := makeAPI(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, c, err := makeAPI(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// yooooooooooooooooooooo...
	f := files.NewReaderFile(ioutil.NopCloser(strings.NewReader("y" + strings.Repeat("o", 3500000))))
	path, err := a.Unixfs().Add(ctx, f)
	if err != nil {
		t.Fatal(err)
	}

	aGetter := &dag.NodeGetter{Dag: a.Dag()}
	info, err := dag.NewInfo(ctx, aGetter, path.Cid())
	if err != nil {
		t.Fatal(err)
	}

	cGetter := &dag.NodeGetter{Dag: c.Dag()}
	cdsync, err := New(cGetter, c.Block())
	if err != nil {
		t.Fatal(err)
	}

	onCompleteCalled := make(chan struct{}, 1)
	onCompleteHook := func(_ context.Context, _ dag.Info, _ map[string]string) error {
		onCompleteCalled <- struct{}{}
		return nil
	}

	removeCheckCalled := make(chan struct{}, 1)
	removeCheckHook := func(_ context.Context, _ dag.Info, _ map[string]string) error {
		removeCheckCalled <- struct{}{}
		return nil
	}

	bGetter := &dag.NodeGetter{Dag: b.Dag()}
	bdsync, err := New(bGetter, b.Block(), func(cfg *Config) {
		cfg.AllowRemoves = true
		cfg.PushPreCheck = func(context.Context, dag.Info, map[string]string) error { return nil }
		cfg.PushComplete = onCompleteHook
		cfg.RemoveCheck = removeCheckHook
	})
	if err != nil {
		t.Fatal(err)
	}

	s := httptest.NewServer(HTTPRemoteHandler(bdsync))
	defer s.Close()

	remoteURL, err := url.Parse(s.URL)
	if err != nil {
		log.Fatal(err)
	}

	rProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = remoteURL.Scheme
			req.URL.Host = remoteURL.Host
			req.Header.Set(httpDsyncProtocolIDHeader, "")
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Set(httpDsyncProtocolIDHeader, "")
			return nil
		},
	}

	proxy := httptest.NewServer(rProxy)
	defer proxy.Close()

	cli := &HTTPClient{URL: proxy.URL + "/dsync"}

	t.Logf("pushing %#v\n", info.Manifest.Nodes)
	push, err := NewPush(aGetter, info, cli, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := push.Do(ctx); err != nil {
		t.Fatal(err)
	}

	// b should now be able to generate a manifest
	_, err = dag.NewManifest(ctx, bGetter, path.Cid())
	if err != nil {
		t.Error(err)
	}

	<-onCompleteCalled

	if err := cli.RemoveCID(ctx, info.RootCID().String(), nil); err != nil {
		t.Fatal(err)
	}

	<-removeCheckCalled

	pull, err := cdsync.NewPull(info.RootCID().String(), proxy.URL+"/dsync", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := pull.Do(ctx); err != nil {
		t.Fatal(err)
	}
}
