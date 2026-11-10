package main

import (
	"encoding/json"
	"testing"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
	"aemeath/agent/internal/websearch"
	"github.com/egoist/mygo/ui"
)

func TestNativeWebSearchShowsExpandableSourcesAndPreservesHistory(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(900, 1100)
	report := brain.WebSearchReport{OK: true, Query: "ESP32-S3 文档", Results: []websearch.Result{{Title: "ESP32-S3 官方文档", URL: "https://docs.espressif.com/", Snippet: "芯片功能与开发指南。"}, {Title: "Espressif", URL: "https://www.espressif.com/", Snippet: "官方产品资料。"}}}
	data, _ := json.Marshal(report)
	f.state.Voice = voice.State{Turn: 1, Phase: "idle", Transcript: "搜索 ESP32-S3 文档", Response: "可以参考官方文档[1]。", ToolCalls: []brain.ToolCall{{ID: 1, Name: "web_search", Status: "succeeded", Result: string(data), Millis: 870}}}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	if !tt.HasText("网页搜索") || !tt.HasText("找到 2 条网页来源 · 展开查看来源") {
		t.Fatal("search not visibly represented in conversation")
	}
	nativeClick(t, tt, "第 1 轮工具详情 网页搜索 #1")
	for i, title := range []string{"ESP32-S3 官方文档", "Espressif"} {
		label := []string{"网页来源 1：", "网页来源 2："}[i] + title
		if _, ok := tt.AccessByRole(ui.RoleLink, label); !ok {
			t.Fatalf("missing clickable source: %s", title)
		}
	}
	if !tt.HasText("芯片功能与开发指南。") || !tt.HasText("docs.espressif.com") {
		t.Fatal("source summary or site missing")
	}
	f.state.Voice = voice.State{Turn: 2, Transcript: "第二问", Response: "第二答", Phase: "idle"}
	a.refresh()
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleLink, "网页来源 1：ESP32-S3 官方文档"); !ok {
		t.Fatal("next turn lost source history")
	}
}

func TestNativeWebSearchRejectsUnsafeLinksAndKeepsCompactControlsVisible(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 577)
	report := brain.WebSearchReport{OK: true, Query: "test", Results: []websearch.Result{{Title: "bad source", URL: "javascript:alert(1)"}}}
	data, _ := json.Marshal(report)
	f.state.Voice = voice.State{Turn: 1, Phase: "thinking", Transcript: "搜索资料", ToolCalls: []brain.ToolCall{{ID: 1, Name: "web_search", Status: "succeeded", Result: string(data), Millis: 10}}}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "第 1 轮工具详情 网页搜索 #1")
	if _, ok := tt.AccessByRole(ui.RoleLink, "网页来源 1：bad source"); ok {
		t.Fatal("unsafe scheme became an actionable source")
	}
	for _, label := range []string{"语音操作栏", "手动唤醒", "取消对话"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 226 || r.X+r.W > 740 || r.Y+r.H > 557 {
			t.Fatalf("search displaced compact control %s: %+v", label, r)
		}
	}
}
