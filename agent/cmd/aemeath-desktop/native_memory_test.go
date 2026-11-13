package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

type nativeLongTermFake struct {
	*nativeVoiceFake
	store     *brain.LongTermMemory
	saveGate  <-chan struct{}
	afterSave func(brain.MemoryEntry)
}

func (f *nativeLongTermFake) ListMemories() brain.MemorySnapshot { return f.store.Snapshot() }
func (f *nativeLongTermFake) SaveMemory(entry brain.MemoryEntry, revision uint64) (brain.MemoryEntry, error) {
	if f.saveGate != nil {
		<-f.saveGate
	}
	saved, err := f.store.Save(entry, revision)
	if err == nil && f.afterSave != nil {
		f.afterSave(saved)
	}
	return saved, err
}
func (f *nativeLongTermFake) DeleteMemory(id string, revision uint64) error {
	return f.store.Delete(id, revision)
}
func (f *nativeLongTermFake) ClearMemories(revision uint64) error { return f.store.Clear(revision) }

func nativeMemoryTest(t *testing.T) (*nativeUI, *nativeLongTermFake, chan func(), *ui.Tester) {
	t.Helper()
	a, base, updates, tt := nativeTest(t)
	f := &nativeLongTermFake{nativeVoiceFake: &nativeVoiceFake{nativeFake: base}, store: brain.OpenLongTermMemory("")}
	a.service = f
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "助手")
	nativeClick(t, tt, "记忆")
	nativeComplete(t, updates, tt)
	return a, f, updates, tt
}

func nativeMemoryButton(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	nativeVoiceClickRole(t, tt, ui.RoleButton, label)
}

func TestNativeMemoryManagerSearchDoesNotUseRecallFallback(t *testing.T) {
	entries := []brain.MemoryEntry{{Key: "饮品", Content: "喜欢绿茶", Category: "preference"}, {Key: "姓名", Content: "小明", Category: "profile"}}
	if len(nativeMemoryEntries(entries, "不存在的内容")) != 0 {
		t.Fatal("manager search returned unrelated recall fallback")
	}
	if got := nativeMemoryEntries(entries, "偏好"); len(got) != 1 || got[0].Key != "饮品" {
		t.Fatal("translated category search failed")
	}
	if got := nativeMemoryEntries(entries, "小明"); len(got) != 1 || got[0].Key != "姓名" {
		t.Fatal("content search failed")
	}
}

func TestNativeMemoriesCreateEditSearchDeleteWhileOffline(t *testing.T) {
	a, f, updates, tt := nativeMemoryTest(t)
	if a.snapshot.Connected || a.snapshot.Voice.Connected {
		t.Fatal("fixture must be offline")
	}
	nativeMemoryButton(t, tt, "新增记忆")
	nativeVoiceEdit(t, tt, "记忆名称", "饮品")
	nativeVoiceEdit(t, tt, "记忆正文", "喜欢绿茶")
	nativeMemoryButton(t, tt, "保存条目")
	nativeComplete(t, updates, tt)
	if a.dirty || len(f.store.Snapshot().Entries) != 1 || !tt.HasText("喜欢绿茶") {
		t.Fatal("offline entry was not saved independently of settings")
	}
	nativeMemoryButton(t, tt, "编辑记忆 饮品")
	nativeVoiceEdit(t, tt, "记忆正文", "喜欢乌龙茶")
	nativeMemoryButton(t, tt, "保存条目")
	nativeComplete(t, updates, tt)
	if f.store.Snapshot().Entries[0].Content != "喜欢乌龙茶" {
		t.Fatal("edit did not persist")
	}
	if !tt.HasText("来源：手动编辑") || !tt.HasText("更新：") {
		t.Fatal("source/time metadata missing")
	}
	nativeVoiceEdit(t, tt, "搜索记忆", "不匹配")
	if tt.HasText("喜欢乌龙茶") || !tt.HasText("暂无匹配的记忆。") {
		t.Fatal("search did not filter list")
	}
	nativeVoiceEdit(t, tt, "搜索记忆", "乌龙")
	if !tt.HasText("喜欢乌龙茶") {
		t.Fatal("content search did not find entry")
	}
	nativeMemoryButton(t, tt, "删除记忆 饮品")
	nativeComplete(t, updates, tt)
	if len(f.store.Snapshot().Entries) != 0 {
		t.Fatal("delete did not persist")
	}
}

func TestNativeMemorySaveCannotAdoptConcurrentDeletionRevision(t *testing.T) {
	a, f, updates, tt := nativeMemoryTest(t)
	gate := make(chan struct{})
	f.saveGate = gate
	f.afterSave = func(saved brain.MemoryEntry) {
		if err := f.store.Delete(saved.ID, f.store.Snapshot().Revision); err != nil {
			t.Error(err)
		}
	}
	nativeMemoryButton(t, tt, "新增记忆")
	nativeVoiceEdit(t, tt, "记忆名称", "饮品")
	nativeVoiceEdit(t, tt, "记忆正文", "喜欢绿茶")
	nativeMemoryButton(t, tt, "保存条目")
	nativeVoiceEdit(t, tt, "记忆正文", "喜欢乌龙茶")
	close(gate)
	nativeComplete(t, updates, tt)
	if !a.memories.editing || a.memories.entry.Content != "喜欢乌龙茶" || a.memories.expected != 1 || a.memories.snapshot.Revision != 2 {
		t.Fatal("save callback adopted an unrelated newer revision or lost draft")
	}
	nativeMemoryButton(t, tt, "保存条目")
	nativeComplete(t, updates, tt)
	if !a.memories.conflict || len(f.store.Snapshot().Entries) != 0 || a.memories.entry.Content != "喜欢乌龙茶" {
		t.Fatal("stale editor silently resurrected deleted memory")
	}
}

func TestNativeMemoryConflictPreservesDraftAndRequiresReview(t *testing.T) {
	a, f, updates, tt := nativeMemoryTest(t)
	seed, err := f.store.Save(brain.MemoryEntry{Key: "饮品", Category: "preference", Content: "喜欢绿茶", Source: "auto"}, f.store.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	a.loadMemories()
	nativeComplete(t, updates, tt)
	nativeMemoryButton(t, tt, "编辑记忆 饮品")
	nativeVoiceEdit(t, tt, "记忆正文", "喜欢乌龙茶")
	seed.Content = "喜欢红茶"
	if _, err := f.store.Save(seed, f.store.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	nativeMemoryButton(t, tt, "保存条目")
	nativeComplete(t, updates, tt)
	if !a.memories.conflict || a.memories.entry.Content != "喜欢乌龙茶" || !a.memories.editing {
		t.Fatal("conflict discarded editor")
	}
	if f.store.Snapshot().Entries[0].Content != "喜欢红茶" || !strings.Contains(a.notice, "修改已保留") {
		t.Fatal("stale edit overwrote current entry or missing error notice")
	}
	nativeMemoryButton(t, tt, "以当前修改重试")
	nativeComplete(t, updates, tt)
	if f.store.Snapshot().Entries[0].Content != "喜欢乌龙茶" || a.memories.editing {
		t.Fatal("reviewed retry did not save")
	}
}

func TestNativeMemorySaveKeepsTypingWhilePending(t *testing.T) {
	a, f, updates, tt := nativeMemoryTest(t)
	nativeMemoryButton(t, tt, "新增记忆")
	nativeVoiceEdit(t, tt, "记忆名称", "饮品")
	nativeVoiceEdit(t, tt, "记忆正文", "喜欢绿茶")
	gate := make(chan struct{})
	f.saveGate = gate
	nativeMemoryButton(t, tt, "保存条目")
	nativeVoiceEdit(t, tt, "记忆正文", "喜欢乌龙茶")
	close(gate)
	nativeComplete(t, updates, tt)
	if !a.memories.editing || a.memories.entry.Content != "喜欢乌龙茶" || a.memories.entry.ID == "" {
		t.Fatal("save callback discarded newer input or lost newly assigned ID")
	}
	if f.store.Snapshot().Entries[0].Content != "喜欢绿茶" {
		t.Fatal("save did not capture immutable input")
	}
	nativeMemoryButton(t, tt, "保存条目")
	nativeComplete(t, updates, tt)
	if len(f.store.Snapshot().Entries) != 1 || f.store.Snapshot().Entries[0].Content != "喜欢乌龙茶" {
		t.Fatal("second save created duplicate or lost newer input")
	}
}

func TestNativeMemoryModeUsesSettingsSaveAndClearRequiresConfirmation(t *testing.T) {
	a, f, updates, tt := nativeMemoryTest(t)
	_, err := f.store.Save(brain.MemoryEntry{Key: "name", Category: "profile", Content: "小明"}, f.store.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	a.loadMemories()
	nativeComplete(t, updates, tt)
	nativeMemoryButton(t, tt, "开启 Agent 工具")
	if !a.draft.Voice.LLMTools || len(f.saves) != 0 {
		t.Fatal("enable shortcut bypassed settings draft")
	}
	nativeClick(t, tt, "仅明确要求")
	if !a.dirty || a.draft.Voice.MemoryMode != "explicit" || len(f.saves) != 0 {
		t.Fatal("mode did not remain a settings draft")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	// The ordinary service refresh also schedules a manager read on this page.
	nativeComplete(t, updates, tt)
	if f.saves[0].Voice.MemoryMode != "explicit" || !f.saves[0].Voice.LLMTools || a.dirty {
		t.Fatal("top settings save omitted memory mode")
	}
	nativeClick(t, tt, "关闭")
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	nativeComplete(t, updates, tt)
	if f.saves[1].Voice.MemoryMode != "off" || len(f.store.Snapshot().Entries) != 1 {
		t.Fatal("off changed stored entries")
	}
	nativeMemoryButton(t, tt, "清空长期记忆")
	if !tt.HasText("清空长期记忆？") || len(f.store.Snapshot().Entries) != 1 {
		t.Fatal("clear bypassed confirmation")
	}
	nativeClick(t, tt, "取消")
	if len(f.store.Snapshot().Entries) != 1 {
		t.Fatal("cancel cleared entries")
	}
	nativeMemoryButton(t, tt, "清空长期记忆")
	nativeClick(t, tt, "确认清空")
	nativeComplete(t, updates, tt)
	if len(f.store.Snapshot().Entries) != 0 {
		t.Fatal("confirmed clear did not persist")
	}
}

func TestNativeConversationClearExplainsAndKeepsLongTermMemory(t *testing.T) {
	a, f, updates, tt := nativeMemoryTest(t)
	_, err := f.store.Save(brain.MemoryEntry{Key: "name", Category: "profile", Content: "小明"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Settings.Voice.Enabled = true
	f.state.Voice = voice.State{Connected: true, Session: 1, Turn: 1, Transcript: "我叫小明", Response: "你好"}
	nativeClick(t, tt, "语音助手")
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "清空对话")
	if !tt.HasText("长期记忆会保留") {
		t.Fatal("clear dialog did not distinguish long term memory")
	}
	nativeClick(t, tt, "清空")
	nativeComplete(t, updates, tt)
	if len(f.store.Snapshot().Entries) != 1 || len(a.voiceHistory) != 0 {
		t.Fatal("conversation clear affected long term memory")
	}
}

func TestNativeMemoryFileErrorRequiresConfirmedRecovery(t *testing.T) {
	a, f, updates, tt := nativeMemoryTest(t)
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	f.store = brain.OpenLongTermMemory(path)
	a.loadMemories()
	nativeComplete(t, updates, tt)
	if !tt.HasText("记忆文件不可用") || !tt.HasText("记忆错误：") {
		t.Fatalf("load failure hidden: snapshot=%+v status=%v error=%v", a.memories.snapshot, tt.HasText("记忆文件不可用"), tt.HasText("记忆错误："))
	}
	info, ok := tt.AccessByRole(ui.RoleButton, "新增记忆")
	if !ok || !info.Disabled {
		t.Fatal("entry creation enabled after load failure")
	}
	nativeMemoryButton(t, tt, "清空长期记忆")
	if !tt.HasText("确认清空会覆盖现有记忆文件") {
		t.Fatal("recovery did not explain destructive replacement")
	}
	nativeClick(t, tt, "取消")
	if f.store.Snapshot().Status != "unavailable" {
		t.Fatal("cancel recovered without approval")
	}
	nativeMemoryButton(t, tt, "清空长期记忆")
	nativeClick(t, tt, "确认清空")
	nativeComplete(t, updates, tt)
	if f.store.Snapshot().Status != "ready" || f.store.Snapshot().Error != "" {
		t.Fatal("confirmed recovery failed")
	}
}
