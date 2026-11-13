package main

import (
	"fmt"
	"strings"

	"aemeath/agent/internal/brain"
	"github.com/egoist/mygo/ui"
)

// Optional so existing hosts and fakes do not need to implement memory management.
type nativeMemoryService interface {
	ListMemories() brain.MemorySnapshot
	SaveMemory(brain.MemoryEntry, uint64) (brain.MemoryEntry, error)
	DeleteMemory(string, uint64) error
	ClearMemories(uint64) error
}

type nativeMemoryState struct {
	snapshot      brain.MemorySnapshot
	loaded        bool
	query         string
	editing       bool
	entry         brain.MemoryEntry
	expected      uint64
	generation    uint64
	conflict      bool
	clearConfirm  bool
	clearExpected uint64
}

func (a *nativeUI) memoryManagerVisible() bool {
	return a.page == "设置" && a.settingsTab == nativeSettingsAssistantTab && a.assistantSettingsSection == 2
}

// Read outside the render/UI thread. Periodic refresh updates extraction status,
// but never changes the editor's revision or its unsaved text.
func (a *nativeUI) loadMemories() {
	service, ok := a.service.(nativeMemoryService)
	if !ok || a.busy["memory-read"] || a.busy["memory-write"] || a.update == nil {
		return
	}
	dispatch := a.update
	a.busy["memory-read"] = true
	go func() {
		snapshot := service.ListMemories()
		dispatch(func() {
			a.busy["memory-read"] = false
			if snapshot.Revision >= a.memories.snapshot.Revision {
				a.memories.snapshot = snapshot
			}
			a.memories.loaded = true
		})
	}()
}

func (a *nativeUI) editMemory(entry brain.MemoryEntry) {
	a.memories.entry = entry
	a.memories.editing = true
	a.memories.expected = a.memories.snapshot.Revision
	a.memories.generation++
	a.memories.conflict = false
}

func (a *nativeUI) mutateMemory(expected uint64, work func(nativeMemoryService) error, done func()) {
	service, ok := a.service.(nativeMemoryService)
	if !ok || a.busy["memory-write"] {
		return
	}
	dispatch := a.update
	if dispatch == nil {
		a.notify(nativeNoticeWarning, "窗口尚未就绪，请稍后重试。")
		return
	}
	a.busy["memory-write"] = true
	go func() {
		err := work(service)
		snapshot := service.ListMemories()
		dispatch(func() {
			a.busy["memory-write"] = false
			if snapshot.Revision >= a.memories.snapshot.Revision {
				a.memories.snapshot = snapshot
			}
			a.memories.loaded = true
			if err != nil {
				a.memories.conflict = snapshot.Revision != expected
				a.notify(nativeNoticeError, "记忆操作失败，修改已保留："+err.Error())
				return
			}
			a.memories.conflict = false
			if done != nil {
				done()
			}
			a.notify(nativeNoticeSuccess, "长期记忆已更新。")
		})
	}()
}

func (a *nativeUI) saveMemory() {
	entry, expected, generation := a.memories.entry, a.memories.expected, a.memories.generation
	var saved brain.MemoryEntry
	a.mutateMemory(expected, func(service nativeMemoryService) (err error) {
		saved, err = service.SaveMemory(entry, expected)
		return err
	}, func() {
		// Typing while a save runs must survive the completion callback.
		if a.memories.generation == generation {
			a.memories.editing = false
		} else if a.memories.editing && a.memories.entry.ID == entry.ID {
			a.memories.entry.ID = saved.ID
			// A later list read may already include another writer's delete.
			// Only advance to the revision committed by this save.
			a.memories.expected = expected + 1
		}
	})
}

func (a *nativeUI) deleteMemory(id string) {
	expected := a.memories.snapshot.Revision
	a.mutateMemory(expected, func(service nativeMemoryService) error {
		return service.DeleteMemory(id, expected)
	}, func() {
		if a.memories.entry.ID == id {
			a.memories.editing = false
		}
	})
}

func (a *nativeUI) clearMemories() {
	expected := a.memories.clearExpected
	a.mutateMemory(expected, func(service nativeMemoryService) error {
		return service.ClearMemories(expected)
	}, func() { a.memories.editing = false })
}

func nativeMemoryStatus(status string) string {
	switch status {
	case "":
		return "等待提炼"
	case "idle":
		return "等待提炼"
	case "ready":
		return "已就绪"
	case "queued":
		return "等待后台提炼"
	case "skipped":
		return "本轮提炼已跳过"
	case "extracting":
		return "正在提炼"
	case "saved", "updated":
		return "提炼已保存"
	case "unchanged":
		return "无新增记忆"
	case "failed", "error":
		return "提炼失败"
	case "unavailable":
		return "记忆文件不可用"
	default:
		return status
	}
}

func nativeMemoryCategory(category string) string {
	switch category {
	case "profile":
		return "资料"
	case "preference":
		return "偏好"
	case "fact":
		return "事实"
	case "goal":
		return "目标"
	default:
		return category
	}
}

func nativeMemoryEntries(entries []brain.MemoryEntry, query string) []brain.MemoryEntry {
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]brain.MemoryEntry, 0, len(entries))
	for _, entry := range brain.SearchMemories(entries, "", 200) {
		text := entry.Key + " " + entry.Content + " " + entry.Category + " " + nativeMemoryCategory(entry.Category)
		if query == "" || strings.Contains(strings.ToLower(text), query) {
			result = append(result, entry)
		}
	}
	return result
}

func nativeMemorySource(source string) string {
	switch source {
	case "manual":
		return "手动编辑"
	case "auto":
		return "自动提炼"
	case "explicit":
		return "明确要求保存"
	default:
		return source
	}
}

func (a *nativeUI) memorySettingsView(c *ui.Context) {
	nativeSettingsSection(c, "memory-mode-card", "长期记忆", func() {
		nativeSettingsField(c, "memory-mode-field", "记忆模式", func() ui.Element {
			modes := []string{"auto", "explicit", "off"}
			selected := 0
			for i, mode := range modes {
				if a.draft.Voice.MemoryMode == mode {
					selected = i
				}
			}
			field := nativeSettingsSegmented(c, "memory-mode", &selected, "记忆模式", "自动", "仅明确要求", "关闭")
			if field.Changed() {
				a.draft.Voice.MemoryMode = modes[selected]
				a.edited()
			}
			return field
		})
		if !a.draft.Voice.LLMTools {
			nativeSettingsActions(c, "memory-enable-tools-row", func() {
				ui.Text(c, "Agent 工具尚未启用").FontSize(11).TextColor(c.Theme().Warning).Grow(1).MinWidth(0).Wrap()
				ui.Button(c.Key("memory-enable-tools"), "开启 Agent 工具").FontSize(11).OnClick(func() { a.draft.Voice.LLMTools = true; a.edited() })
			})
		}
	})
	if _, ok := a.service.(nativeMemoryService); !ok {
		ui.Text(c, "当前服务不支持记忆管理，请更新桌面应用。").Wrap()
		return
	}
	if !a.memories.loaded {
		a.loadMemories()
	}
	m := &a.memories
	busy := a.busy["memory-write"]
	unavailable := m.snapshot.Status == "unavailable"
	nativeSettingsSection(c, "memory-manager-card", "记忆管理", func() {
		ui.Text(c, "提炼状态："+nativeMemoryStatus(m.snapshot.Status)).FontSize(12).Wrap()
		if len(m.snapshot.Entries) >= 200 {
			ui.Text(c, "记忆已满（200 条），删除部分条目后可继续新增；已有条目仍可编辑。").TextColor(c.Theme().Warning).FontSize(12).Wrap()
		}
		if m.snapshot.Error != "" {
			ui.Text(c, "记忆错误："+m.snapshot.Error).TextColor(c.Theme().Warning).FontSize(12).Wrap()
		}
		nativeSettingsActions(c, "memory-actions", func() {
			ui.TextInput(c.Key("memory-query"), &m.query).Label("搜索记忆").Placeholder("搜索关键词、分类或正文").Grow(1).MinWidth(160).Height(36)
			ui.Button(c.Key("memory-refresh"), "刷新列表").Disabled(a.busy["memory-read"] || busy).OnClick(a.loadMemories)
			ui.Button(c.Key("memory-add"), "新增记忆").Disabled(busy || unavailable || !m.loaded).OnClick(func() { a.editMemory(brain.MemoryEntry{Category: "fact"}) })
			ui.Button(c.Key("memory-clear"), "清空长期记忆").Disabled(busy || !m.loaded || (!unavailable && len(m.snapshot.Entries) == 0)).OnClick(func() {
				m.clearExpected = m.snapshot.Revision
				m.clearConfirm = true
			})
		})
		if !m.loaded {
			ui.Text(c, "正在读取记忆…").FontSize(12)
			return
		}
		if m.editing {
			a.memoryEditorView(c)
		}
		entries := nativeMemoryEntries(m.snapshot.Entries, m.query)
		ui.Text(c, fmt.Sprintf("%d 条记忆", len(entries))).FontSize(12).TextColor(c.Theme().TextMuted)
		if len(entries) == 0 {
			ui.Text(c, "暂无匹配的记忆。").FontSize(12).TextColor(c.Theme().TextMuted)
		}
		for _, entry := range entries {
			ui.Column(c.Key("memory-"+entry.ID)).FillWidth().MinWidth(0).Gap(8).Padding(12).Radius(8).Border(1, c.Theme().Border).Children(func() {
				ui.Text(c, entry.Key+" · "+nativeMemoryCategory(entry.Category)).Bold().FontSize(13).Wrap()
				ui.Text(c, entry.Content).FontSize(13).Wrap()
				updated := "未知时间"
				if !entry.UpdatedAt.IsZero() {
					updated = entry.UpdatedAt.Local().Format("2006-01-02 15:04")
				}
				ui.Text(c, "来源："+nativeMemorySource(entry.Source)+" · 更新："+updated).FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
				nativeSettingsActions(c, "entry-actions", func() {
					ui.Button(c.Key("edit"), "编辑").Label("编辑记忆 " + entry.Key).Disabled(busy || unavailable).OnClick(func() { a.editMemory(entry) })
					ui.Button(c.Key("delete"), "删除").Label("删除记忆 " + entry.Key).Disabled(busy || unavailable).OnClick(func() { a.deleteMemory(entry.ID) })
				})
			})
		}
	})
	clearDescription := "所有长期条目将被删除，且无法恢复。旧聊天原文会保留，之后的对话仍可能重新提炼记忆。"
	if unavailable {
		clearDescription = "记忆文件不可用。确认清空会覆盖现有记忆文件并恢复为空列表，原内容将无法恢复。旧聊天原文会保留。"
	}
	if ui.AlertDialog(c, &m.clearConfirm, "清空长期记忆？", clearDescription, "取消", "确认清空") == 1 {
		a.clearMemories()
	}
}

func (a *nativeUI) memoryEditorView(c *ui.Context) {
	m := &a.memories
	ui.Column(c.Key("memory-editor")).Label("记忆编辑器").FillWidth().MinWidth(0).Gap(12).Children(func() {
		title := "新增记忆"
		if m.entry.ID != "" {
			title = "编辑记忆"
		}
		ui.Text(c, title).Bold().FontSize(14)
		for _, field := range []struct {
			key, title string
			value      *string
		}{
			{"key", "记忆名称", &m.entry.Key},
		} {
			nativeSettingsField(c, "memory-"+field.key+"-field", field.title, func() ui.Element {
				input := ui.TextInput(c.Key("memory-"+field.key), field.value).Label(field.title).FillWidth().MinWidth(0).Height(36)
				if input.Changed() {
					m.generation++
				}
				return input
			})
		}
		nativeSettingsField(c, "memory-category-field", "记忆分类", func() ui.Element {
			categories := []string{"profile", "preference", "fact", "goal"}
			selected := 2
			for i, category := range categories {
				if m.entry.Category == category {
					selected = i
				}
			}
			field := nativeSettingsSegmented(c, "memory-category", &selected, "记忆分类", "资料", "偏好", "事实", "目标")
			if field.Changed() {
				m.entry.Category = categories[selected]
				m.generation++
			}
			return field
		})
		nativeSettingsTextArea(c, "memory-content-field", "记忆正文", func() ui.Element {
			field := ui.TextArea(c.Key("memory-content"), &m.entry.Content).Label("记忆正文").Placeholder("填写长期偏好或事实（最多 256 字符）").FillWidth().MinWidth(0).Height(90)
			if field.Changed() {
				m.generation++
			}
			return field
		})
		ui.Text(c, fmt.Sprintf("%d / 256 字符", len([]rune(m.entry.Content)))).FontSize(11).TextColor(c.Theme().TextMuted)
		if m.conflict {
			ui.Text(c, "记忆列表已变化。当前修改已保留；请检查最新列表，再决定是否用当前修改重试。").FontSize(12).TextColor(c.Theme().Warning).Wrap()
			ui.Button(c.Key("memory-retry"), "以当前修改重试").Disabled(a.busy["memory-write"]).OnClick(func() {
				m.expected = m.snapshot.Revision
				a.saveMemory()
			})
		}
		nativeSettingsActions(c, "memory-editor-actions", func() {
			ui.Button(c.Key("memory-save"), "保存条目").Disabled(a.busy["memory-write"] || m.conflict || strings.TrimSpace(m.entry.Key) == "" || strings.TrimSpace(m.entry.Content) == "" || len([]rune(m.entry.Content)) > 256).OnClick(a.saveMemory)
			ui.Button(c.Key("memory-cancel"), "取消编辑").OnClick(func() { m.editing = false; m.generation++ })
		})
	})
}
