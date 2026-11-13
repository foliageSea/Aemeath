package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/timeaware"
)

func fixtureTimeInfo(t *testing.T, timezone, source string) timeaware.Info {
	t.Helper()
	location, err := time.LoadLocation(timezone)
	if err != nil {
		t.Fatal(err)
	}
	utc := time.Date(2026, 11, 10, 1, 30, 0, 0, time.UTC)
	local := utc.In(location)
	weekdays := []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	info := timeaware.Info{DateTime: local.Format(time.RFC3339), UTC: utc.Format(time.RFC3339), Unix: utc.Unix(), Timezone: timezone, UTCOffset: local.Format("-07:00"), Weekday: weekdays[local.Weekday()], Date: local.Format("2006-01-02"), Time: local.Format("15:04:05"), IsDST: local.IsDST(), Source: source, Provider: "timeapi.io"}
	if source == "system_fallback" {
		info.Provider = "system"
		info.Notice = "外部时间服务不可用，使用电脑系统时间"
	}
	return info
}

func TestParseCurrentTimeReportHistoricalResults(t *testing.T) {
	for _, source := range []string{"network", "system_fallback"} {
		t.Run(source, func(t *testing.T) {
			want := CurrentTimeReport{OK: true, Info: fixtureTimeInfo(t, "Asia/Shanghai", source)}
			data, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			report, ok := ParseCurrentTimeReport(string(data))
			if !ok || report != want {
				t.Fatalf("historical report lost time/source metadata: %+v valid=%t", report, ok)
			}
			if !strings.Contains(report.Summary(), "2026-11-10 09:30:00") || !strings.Contains(report.Summary(), "星期二") {
				t.Fatalf("invalid historical summary: %q", report.Summary())
			}
		})
	}
	report, ok := ParseCurrentTimeReport(`{"ok":false,"message":"当前时间查询失败，请稍后重试。"}`)
	if !ok || report.OK || report.Summary() != report.Message {
		t.Fatalf("historical failure report: %+v valid=%t", report, ok)
	}
}

func TestParseCurrentTimeReportRejectsInvalidResults(t *testing.T) {
	for _, raw := range []string{"[]", "{}", "not JSON", strings.Repeat("x", 4097), string([]byte{0xff})} {
		if _, ok := ParseCurrentTimeReport(raw); ok {
			t.Errorf("accepted invalid report %q", raw)
		}
	}
	mutations := []struct {
		name   string
		change func(*CurrentTimeReport)
	}{
		{"inconsistent time", func(r *CurrentTimeReport) { r.Info.Time = "wrong" }},
		{"wrong weekday", func(r *CurrentTimeReport) { r.Info.Weekday = "星期日" }},
		{"wrong unix", func(r *CurrentTimeReport) { r.Info.Unix++ }},
		{"invalid timezone", func(r *CurrentTimeReport) { r.Info.Timezone = "" }},
		{"wrong network provider", func(r *CurrentTimeReport) { r.Info.Provider = "system" }},
		{"fallback without notice", func(r *CurrentTimeReport) { r.Info = fixtureTimeInfo(t, "UTC", "system_fallback"); r.Info.Notice = "" }},
		{"oversized message", func(r *CurrentTimeReport) { r.Message = strings.Repeat("x", 513) }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			report := CurrentTimeReport{OK: true, Info: fixtureTimeInfo(t, "UTC", "network")}
			tc.change(&report)
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := ParseCurrentTimeReport(string(data)); ok {
				t.Fatalf("accepted invalid historical report %s", data)
			}
		})
	}
}

func assertNoCurrentTimeToolRequest(t *testing.T, req map[string]any) {
	t.Helper()
	for _, key := range []string{"tools", "messages"} {
		data, err := json.Marshal(req[key])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "get_current_time") {
			t.Errorf("removed time tool remains in outgoing %s: %s", key, data)
		}
	}
}

func TestCurrentTimeSnapshotReplyAndStreamWithoutTimeTool(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			now := time.Date(2026, 11, 10, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
			var requests, clockCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				assertNoCurrentTimeToolRequest(t, req)
				assertTimeSnapshotRequest(t, req, timeSnapshotPrompt(now))
				n := requests.Add(1)
				schemas, _ := req["tools"].([]any)
				if n == 1 && len(schemas) == 0 {
					t.Error("regression must inspect an enabled tool schema")
				}
				if req["stream"] == true {
					sseChunk(w, map[string]any{"role": "assistant", "content": "北京时间上午九点半，星期二。"}, nil)
					sseEnd(w)
				} else if streaming {
					replyJSON(w, "READY")
				} else {
					replyJSON(w, "北京时间上午九点半，星期二。")
				}
			}))
			defer server.Close()
			engine, err := New(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test", Persona: "测试助手", Tools: true}, &testHost{})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			engine.timeNow = func() time.Time { clockCalls.Add(1); return now }
			var answer, deltas string
			if streaming {
				answer, err = engine.ReplyStream(context.Background(), "北京时间几点？", func(delta string) error { deltas += delta; return nil })
			} else {
				answer, err = engine.Reply(context.Background(), "北京时间几点？")
			}
			wantRequests := int32(1)
			if streaming {
				wantRequests = 2
			}
			if err != nil || answer != "北京时间上午九点半，星期二。" || requests.Load() != wantRequests || clockCalls.Load() != 1 {
				t.Fatalf("answer=%q err=%v requests=%d clock samples=%d", answer, err, requests.Load(), clockCalls.Load())
			}
			if streaming && deltas != answer {
				t.Fatal("time metadata leaked into speech deltas")
			}
			assertTimeSnapshotTransient(t, engine, timeSnapshotPrompt(now), 1)
		})
	}
}
