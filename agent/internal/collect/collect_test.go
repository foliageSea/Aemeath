package collect

import (
	"encoding/json"
	"strings"
	"testing"

	"aemeath/agent/internal/proto"
)

func TestRate(t *testing.T) {
	if r := rate(100, 300, 2); r == nil || *r != 100 {
		t.Fatalf("rate = %v, want 100", r)
	}
	if rate(300, 100, 1) != nil {
		t.Fatal("counter reset should yield nil")
	}
	if rate(0, 100, 0) != nil {
		t.Fatal("zero interval should yield nil")
	}
}

func TestFrameOmitsMissing(t *testing.T) {
	f := proto.Frame{V: 1, Host: "h", OS: "darwin", TS: 1, CPU: &proto.CPU{Total: proto.F(12.5)}}
	b, _ := json.Marshal(f)
	s := string(b)
	for _, absent := range []string{`"gpu"`, `"temp"`, `"mem"`, `"uptime"`} {
		if strings.Contains(s, absent) {
			t.Errorf("%s should be omitted: %s", absent, s)
		}
	}
	if !strings.Contains(s, `"cpu":{"total":12.5}`) {
		t.Errorf("unexpected cpu encoding: %s", s)
	}
}

func TestPhysicalIface(t *testing.T) {
	for name, want := range map[string]bool{"en0": true, "lo0": false, "utun3": false, "Wi-Fi": true, "vEthernet (WSL)": false} {
		if got := physicalIface(name); got != want {
			t.Errorf("physicalIface(%q) = %v, want %v", name, got, want)
		}
	}
}
