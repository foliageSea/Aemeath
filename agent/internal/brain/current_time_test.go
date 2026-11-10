package brain

import (
	"context"
	"encoding/json"
	"errors"
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

func timeArgs(t *testing.T, zone string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{"timezone": zone})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCurrentTimeToolNetworkFallbackAndTimezoneAlias(t *testing.T) {
	for _, source := range []string{"network", "system_fallback"} {
		t.Run(source, func(t *testing.T) {
			var events []ToolCall
			ctx := WithToolObserver(context.Background(), func(call ToolCall) { events = append(events, call) })
			calls := 0
			tool := currentTimeTool(func(ctx context.Context, zone string) (timeaware.Info, error) {
				calls++
				if zone != "Asia/Shanghai" {
					t.Fatalf("alias not normalized: %s", zone)
				}
				return fixtureTimeInfo(t, zone, source), nil
			})
			output, err := tool.InvokableRun(ctx, timeArgs(t, "北京时间"))
			report, ok := ParseCurrentTimeReport(output)
			if err != nil || !ok || !report.OK || report.Info.Time != "09:30:00" || report.Info.Weekday != "星期二" || report.Info.Source != source || calls != 1 {
				t.Fatalf("invalid time response %s %v", output, err)
			}
			if len(events) != 2 || events[0].Status != "running" || events[1].Status != "succeeded" {
				t.Fatalf("time event lifecycle %+v", events)
			}
			display, ok := ParseCurrentTimeReport(events[1].Result)
			if !ok || display.Info != report.Info {
				t.Fatal("display lost source/time data")
			}
			if source == "system_fallback" && (report.Info.Provider != "system" || report.Info.Notice == "") {
				t.Fatal("fallback pretends to be network time")
			}
		})
	}
}

func TestCurrentTimeToolInvalidTimezoneCancelAndInvalidReport(t *testing.T) {
	calls := 0
	tool := currentTimeTool(func(ctx context.Context, zone string) (timeaware.Info, error) {
		calls++
		return fixtureTimeInfo(t, "UTC", "network"), nil
	})
	for _, args := range []string{timeArgs(t, "Mars/Olympus"), timeArgs(t, strings.Repeat("x", 129)), "[]"} {
		output, err := tool.InvokableRun(context.Background(), args)
		report, ok := ParseCurrentTimeReport(output)
		if err != nil || !ok || report.OK {
			t.Fatalf("invalid timezone accepted %s %v", output, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid timezone reached time service")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.InvokableRun(ctx, "{}"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation hidden")
	}
	if calls != 0 {
		t.Fatal("canceled request reached time service")
	}
	info := fixtureTimeInfo(t, "UTC", "network")
	info.Time = "wrong"
	output, err := currentTimeTool(func(context.Context, string) (timeaware.Info, error) { return info, nil }).InvokableRun(context.Background(), "{}")
	report, ok := ParseCurrentTimeReport(output)
	if err != nil || !ok || report.OK {
		t.Fatal("inconsistent external time accepted")
	}
}

func TestCurrentTimeRealReActReplyAndStream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var requests, queries atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				msgs := req["messages"].([]any)
				if !strings.Contains(msgs[0].(map[string]any)["content"].(string), "get_current_time") {
					t.Error("missing temporal grounding instructions")
				}
				if requests.Add(1) == 1 {
					callsJSON(w, []any{call("time", "get_current_time", timeArgs(t, "Asia/Shanghai"))})
					return
				}
				found := false
				for _, raw := range msgs {
					message := raw.(map[string]any)
					if message["role"] == "tool" {
						report, ok := ParseCurrentTimeReport(message["content"].(string))
						if ok && report.OK && report.Info.Time == "09:30:00" && report.Info.Source == "network" {
							found = true
						}
					}
				}
				if !found {
					t.Error("current external time did not reach model")
				}
				if req["stream"] == true {
					sseChunk(w, map[string]any{"role": "assistant", "content": "北京时间上午九点半，星期二。"}, nil)
					sseEnd(w)
				} else {
					if streaming {
						replyJSON(w, "READY")
					} else {
						replyJSON(w, "北京时间上午九点半，星期二。")
					}
				}
			}))
			defer server.Close()
			engine, err := newWithTime(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test", Tools: true}, &testHost{}, func(ctx context.Context, zone string) (timeaware.Info, error) {
				queries.Add(1)
				return fixtureTimeInfo(t, zone, "network"), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var answer, deltas string
			if streaming {
				answer, err = engine.ReplyStream(context.Background(), "北京时间几点？", func(delta string) error { deltas += delta; return nil })
			} else {
				answer, err = engine.Reply(context.Background(), "北京时间几点？")
			}
			if err != nil || queries.Load() != 1 || answer != "北京时间上午九点半，星期二。" {
				t.Fatalf("answer=%q err=%v queries=%d", answer, err, queries.Load())
			}
			if streaming && deltas != answer {
				t.Fatal("time metadata leaked into speech deltas")
			}
		})
	}
}
