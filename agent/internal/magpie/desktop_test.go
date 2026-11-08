package magpie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFullQuotaBeforeDeviceSelection(t *testing.T) {
	accounts := make([]map[string]any, 10)
	for i := range accounts {
		accounts[i] = map[string]any{"name": fmt.Sprintf("订阅 · Account %d", i), "plan": "中文方案", "windows": []map[string]any{{"name": "窗口 · five hours", "display": strings.Repeat("1", 30), "used": 10}}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/magpie/quotas" {
			json.NewEncoder(w).Encode(map[string]any{"data": accounts})
			return
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	full, err := New(srv.URL).FrameAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Accounts) != 10 || full.Accounts[9].Name != "订阅 · Account 9" || full.Accounts[9].Plan != "中文方案" {
		t.Fatalf("full identities lost: %+v", full.Accounts)
	}
	// The UI can promote an account beyond the old first-eight cutoff.
	full.Accounts[0], full.Accounts[9] = full.Accounts[9], full.Accounts[0]
	wire := ForDevice(full)
	if len(wire.Accounts) != 8 || wire.Accounts[0].Name != "- Account 9" {
		t.Fatalf("device selection: %+v", wire.Accounts)
	}
	if len(wire.Accounts[0].Windows[0].Display) != maxDisplay {
		t.Fatal("display exceeds firmware buffer")
	}
	wire.Accounts[0].Windows[0].Name = "mutated"
	if full.Accounts[0].Windows[0].Name != "窗口 · five hours" {
		t.Fatal("device conversion mutated source")
	}
	if full.Accounts[0].Name != "订阅 · Account 9" {
		t.Fatal("device conversion mutated account identity")
	}
}
