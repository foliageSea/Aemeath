package magpie

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const quotasFixture = `{"object":"list","data":[
 {"provider":"codex","name":"Codex","kind":"subscription","plan":"plus","windows":[
   {"name":"5 hours","used":0,"remaining":100,"resetsAt":"2026-11-06T02:11:22+08:00","holdsWhy":"low"},
   {"name":"7 days","used":11,"remaining":89,"resetsAt":"2026-11-11T18:27:45+08:00"}]},
 {"provider":"copilot","name":"Copilot","kind":"subscription","plan":"Pro+","windows":[
   {"name":"Premium requests","used":50.5,"remaining":49.5,"resetsAt":"2026-12-01T00:00:00Z","display":"3535 / 7000"},
   {"unlimited":true,"name":"Chat requests","used":0,"remaining":100,"display":"Unlimited"}]},
 {"provider":"devin","name":"Devin","kind":"subscription","windows":[{"name":"1 day","used":0,"remaining":100}]},
 {"provider":"antigravity","name":"Antigravity","plan":"Google AI Pro","windows":[
   {"name":"Gemini · 5 hours","used":1}, {"name":"w2","used":0}, {"name":"w3","used":0}, {"name":"w4","used":0},
   {"name":"w5","used":0}, {"name":"w6","used":0}, {"name":"w7","used":0}, {"name":"w8","used":0}, {"name":"w9","used":0}]}]}`

const usageFixture = `{"period":"30d","calls":11450,"cost":263.43,"series":[{"calls":5,"cost":1},{"calls":410,"cost":12.5}]}`

func server(t *testing.T, usage bool) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/magpie/quotas", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(quotasFixture)) })
	if usage {
		mux.HandleFunc("/v1/magpie/usage", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(usageFixture)) })
	}
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestFrame(t *testing.T) {
	f, err := New(server(t, true).URL).Frame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != "quota" || len(f.Accounts) != 4 {
		t.Fatalf("type=%q accounts=%d", f.Type, len(f.Accounts))
	}
	codex := f.Accounts[0]
	if codex.Windows[0].Reset != 1793902282 {
		t.Errorf("reset = %d, want 1793902282", codex.Windows[0].Reset)
	}
	cop := f.Accounts[1]
	if len(cop.Windows) != 1 || cop.Windows[0].Display != "3535 / 7000" || cop.Windows[0].Used != 50.5 {
		t.Errorf("copilot windows = %+v (unlimited should be dropped)", cop.Windows)
	}
	if f.Accounts[2].Plan != "" || f.Accounts[2].Windows[0].Reset != 0 {
		t.Errorf("devin missing fields should stay empty: %+v", f.Accounts[2])
	}
	ag := f.Accounts[3]
	if len(ag.Windows) != maxWindows || ag.Windows[0].Name != "Gemini - 5 hours" {
		t.Errorf("antigravity: n=%d first=%q", len(ag.Windows), ag.Windows[0].Name)
	}
	if f.Usage == nil || f.Usage.CallsToday != 410 || f.Usage.Cost30d != 263.43 {
		t.Errorf("usage = %+v", f.Usage)
	}
}

func TestFrameWithoutUsage(t *testing.T) {
	f, err := New(server(t, false).URL).Frame(context.Background())
	if err != nil || f.Usage != nil || len(f.Accounts) != 4 {
		t.Fatalf("err=%v usage=%v", err, f.Usage)
	}
}

func TestASCII(t *testing.T) {
	if got := ascii("Claude & GPT · 7 days", 31); got != "Claude & GPT - 7 days" {
		t.Errorf("got %q", got)
	}
	if got := ascii("日本 Pro", 31); got != "Pro" {
		t.Errorf("got %q", got)
	}
	if got := ascii(strings.Repeat("x", 40), 31); len(got) != 31 {
		t.Errorf("len %d", len(got))
	}
}
