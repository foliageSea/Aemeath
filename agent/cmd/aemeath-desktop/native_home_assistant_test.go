package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/hamcp"
	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

func TestNativeHASettingsMaskedProbeSaveAndNavigation(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(900, 1200)
	a.draft.Voice.HAMCPEnabled = true
	a.draft.Voice.HAMCPURL = "http://ha.local:8123"
	a.draft.Voice.HAAccessToken = "fixture-ha-token"
	a.haProbe = func(ctx context.Context, cfg hamcp.Config) ([]hamcp.Tool, error) {
		if cfg.Endpoint != "http://ha.local:8123/api/mcp/assist" || cfg.Token != "fixture-ha-token" {
			t.Error("probe lost endpoint or token")
		}
		return []hamcp.Tool{{Name: "HassTurnOn", Title: "开启设备"}, {Name: "GetLiveContext", Title: "家居状态"}}, nil
	}
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "智能家居")
	info, ok := tt.AccessByRole(ui.RoleTextField, "HA 长期访问令牌")
	if !ok || !info.Password || strings.Contains(info.Value, "fixture-ha-token") {
		t.Fatal("HA token is not masked")
	}
	nativeClick(t, tt, "显示 HA 长期访问令牌")
	info, _ = tt.AccessByRole(ui.RoleTextField, "HA 长期访问令牌")
	if info.Password || info.Value != "fixture-ha-token" {
		t.Fatal("HA token reveal failed")
	}
	nativeClick(t, tt, "隐藏 HA 长期访问令牌")
	nativeClick(t, tt, "测试 HA 连接")
	nativeComplete(t, updates, tt)
	for _, text := range []string{"连接成功 · 2 个可用工具", "开启设备", "家居状态", "Agent 工具尚未启用"} {
		if !tt.HasText(text) {
			t.Fatalf("missing HA setup state %s", text)
		}
	}
	nativeClick(t, tt, "开启 Agent 工具")
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || !f.saves[0].Voice.HAMCPEnabled || !f.saves[0].Voice.LLMTools || f.saves[0].Voice.HAAccessToken != "fixture-ha-token" {
		t.Fatal("HA configuration did not save")
	}
	nativeClick(t, tt, "概览")
	nativeClick(t, tt, "HA 连接详情")
	if a.page != "设置" || a.settingsTab != 4 || !tt.HasText("Home Assistant") {
		t.Fatal("HA sidebar entry did not open settings")
	}
}

func TestNativeHAProbeIgnoresEditsDuringRequestAndReportsFailure(t *testing.T) {
	a, _, updates, tt := nativeTest(t)
	tt.SetSize(900, 1000)
	a.draft.Voice.HAAccessToken = "first-token"
	entered, release := make(chan struct{}), make(chan struct{})
	a.haProbe = func(context.Context, hamcp.Config) ([]hamcp.Tool, error) {
		close(entered)
		<-release
		return nil, hamcp.ErrUnauthorized
	}
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "智能家居")
	nativeClick(t, tt, "测试 HA 连接")
	<-entered
	a.draft.Voice.HAAccessToken = "new-token"
	a.edited()
	tt.Frame()
	close(release)
	nativeComplete(t, updates, tt)
	if a.homeAssistant.checked || a.busy["ha-probe"] || a.draft.Voice.HAAccessToken != "new-token" {
		t.Fatal("stale HA probe replaced newer draft")
	}
	a.haProbe = func(context.Context, hamcp.Config) ([]hamcp.Tool, error) { return nil, hamcp.ErrUnauthorized }
	nativeClick(t, tt, "测试 HA 连接")
	nativeComplete(t, updates, tt)
	if !tt.HasText("连接失败") || tt.HasText("连接成功") {
		t.Fatal("failed HA probe displayed success")
	}
}

func TestNativeHASidebarToolCountAndZeroToolWarning(t *testing.T) {
	for _, probe := range []bool{false, true} {
		for _, count := range []int{0, 2} {
			name := fmt.Sprintf("probe=%t/tools=%d", probe, count)
			t.Run(name, func(t *testing.T) {
				a, f, _, tt := nativeTest(t)
				tt.SetSize(760, 900)
				f.state.Settings.Voice.HAMCPEnabled = true
				f.state.Settings.Voice.HAMCPURL = "http://ha.local:8123"
				a.draft.Voice = f.state.Settings.Voice
				status := hamcp.Status{Connected: true, ToolCount: count}
				if !probe {
					f.state.Voice.HomeAssistant = status
				}
				a.refresh()
				if probe {
					a.homeAssistant = nativeHAState{checked: true, config: nativeHAConfig(a.draft.Voice), status: status, runtimeAtCheck: a.snapshot.Voice.HomeAssistant}
				}
				tt.Frame()
				label := fmt.Sprintf("HA · %d 个可用工具", count)
				if !tt.HasText(label) || tt.HasText("HA · 最近可用") {
					t.Fatalf("missing accurate HA sidebar tool count: %s", label)
				}
				nativeClick(t, tt, "HA 连接详情")
				if count == 0 {
					for _, text := range []string{"已连接 · 0 个可用工具", "尚无可用工具，暂不能通过 HA 控制设备。请检查 MCP 工具集配置及权限。"} {
						if !tt.HasText(text) {
							t.Fatalf("zero-tool connection missing warning: %s", text)
						}
					}
					if tt.HasText("连接成功") || tt.HasText("最近连接可用") {
						t.Fatal("zero-tool connection misleadingly reports readiness")
					}
				} else if tt.HasText("尚无可用工具") {
					t.Fatal("usable tools incorrectly reported unavailable")
				}
			})
		}
	}
}

func TestNativeHASidebarRuntimeFailureSupersedesProbeCount(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.state.Settings.Voice.HAMCPEnabled = true
	a.draft.Voice = f.state.Settings.Voice
	a.refresh()
	a.homeAssistant = nativeHAState{checked: true, config: nativeHAConfig(a.draft.Voice), status: hamcp.Status{Connected: true, ToolCount: 2}, runtimeAtCheck: a.snapshot.Voice.HomeAssistant}
	tt.Frame()
	if !tt.HasText("HA · 2 个可用工具") {
		t.Fatal("probe tool count not displayed")
	}
	f.state.Voice.HomeAssistant = hamcp.Status{Error: "连接已中断"}
	a.refresh()
	tt.Frame()
	if !tt.HasText("HA · 连接异常") || tt.HasText("HA · 2 个可用工具") {
		t.Fatal("stale probe count hides runtime failure")
	}
}

func TestNativeHASettingsCompactAndDynamicTraceTitle(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 577)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "智能家居")
	for _, label := range []string{"HA MCP 服务地址", "HA 长期访问令牌", "显示 HA 长期访问令牌", "测试 HA 连接", "保存设置"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 226 || r.X+r.W > 740 || r.Y < 20 || r.Y+r.H > 557 {
			t.Fatalf("HA setup control overflows compact window: %s %+v", label, r)
		}
	}
	report := brain.HomeAssistantReport{OK: true, Tool: "ha_HassTurnOn_example", Text: "HA 已接受客厅灯开启请求", Message: "设备状态以 HA 上报为准"}
	data, _ := json.Marshal(report)
	f.state.Voice = voice.State{Turn: 1, Phase: "thinking", Transcript: "打开客厅灯", ToolCalls: []brain.ToolCall{{ID: 1, Name: report.Tool, Title: "HA · 开启设备", Status: "succeeded", Result: string(data), Arguments: "{客厅灯}", Millis: 210}}}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	if !tt.HasText("HA · 开启设备") {
		t.Fatal("remote tool title missing")
	}
	nativeClick(t, tt, "第 1 轮工具详情 HA · 开启设备 #1")
	if !tt.HasText(report.Text) || !tt.HasText(report.Message) {
		t.Fatal("HA result was not rendered as readable text")
	}
}
