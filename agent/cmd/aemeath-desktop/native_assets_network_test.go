package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/netproxy"
	"aemeath/agent/internal/wake"
)

type nativeAssetsNetworkService struct {
	*nativeFake
	router *netproxy.Router
}

func (s *nativeAssetsNetworkService) NetworkContext(ctx context.Context) context.Context {
	return netproxy.WithRouter(ctx, s.router)
}

func TestWakeInstallationUsesSavedNetworkNotDraft(t *testing.T) {
	var savedCalls, draftCalls atomic.Int32
	saved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { savedCalls.Add(1); fmt.Fprint(w, "saved-policy") }))
	defer saved.Close()
	draft := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { draftCalls.Add(1); fmt.Fprint(w, "wrong-policy") }))
	defer draft.Close()
	cfg := netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: saved.URL}
	router, err := netproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	a, f, updates, tt := nativeTest(t)
	f.state.Settings.Network = cfg
	a.service = &nativeAssetsNetworkService{nativeFake: f, router: router}
	a.draft.Network = netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: draft.URL}
	a.edited()
	a.wakeAssetOps.install = func(ctx context.Context, _ string, _ func(wake.InstallProgress)) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://models.example.test/fixture", nil)
		if err != nil {
			return err
		}
		res, err := (&http.Client{Transport: netproxy.Transport(nil), Timeout: time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			return err
		}
		if string(body) != "saved-policy" {
			return fmt.Errorf("installer did not use saved routing")
		}
		return nil
	}
	a.startWakeAssets(true)
	nativeFinishWake(t, a, updates, tt)
	if !a.wakeAssets.ready || savedCalls.Load() != 1 || draftCalls.Load() != 0 {
		t.Fatal("resource install ignored saved policy")
	}
	if a.draft.Network.ProxyURL != draft.URL || !a.dirty || len(f.saves) != 0 {
		t.Fatal("installation changed or saved network draft")
	}
}
