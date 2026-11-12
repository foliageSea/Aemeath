package main

import (
	"fmt"

	"aemeath/agent/internal/pet"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Both the tray and the character expose the same actions. In particular,
// recovery remains reachable when the whole character is click-through.
type petMenuEntry struct {
	id, label                       string
	check, radio, checked, disabled bool
	action                          func()
	children                        []petMenuEntry
}

func (p *nativePet) menuEntries() []petMenuEntry {
	observed := p.assistant
	entries := []petMenuEntry{
		{id: "pet-recover", label: "显示桌宠 / 恢复鼠标交互", action: p.recover},
		{id: "pet-visible", label: "显示桌宠", check: true, checked: p.config.Visible, action: func() { p.setVisible(!p.config.Visible) }},
		{id: "pet-paused", label: "暂停自主活动", check: true, checked: p.paused, action: func() { p.setPaused(!p.paused) }},
		{id: "pet-wander", label: "允许桌面散步", check: true, checked: p.config.Wander, action: func() { p.changeConfig(func(c *pet.Config) { c.Wander = !c.Wander }) }},
		{id: "pet-top", label: "始终置顶", check: true, checked: p.config.AlwaysOnTop, action: func() { p.changeConfig(func(c *pet.Config) { c.AlwaysOnTop = !c.AlwaysOnTop }) }},
		{id: "pet-ignore", label: "鼠标穿透（从托盘恢复）", check: true, checked: p.ignoreMouse, disabled: !p.canIgnoreMouse, action: func() { p.setIgnoreMouse(!p.ignoreMouse) }},
	}
	var sizes []petMenuEntry
	for _, s := range []float64{.75, 1, 1.25, 1.5} {
		scale := s
		sizes = append(sizes, petMenuEntry{id: fmt.Sprintf("pet-scale-%g", scale), label: fmt.Sprintf("%.0f%%", scale*100), radio: true, checked: p.config.Scale == scale, action: func() { p.changeConfig(func(c *pet.Config) { c.Scale = scale }) }})
	}
	var frequencies []petMenuEntry
	for _, f := range []struct {
		value pet.Frequency
		label string
	}{{pet.Slow, "慢"}, {pet.Standard, "标准"}, {pet.Fast, "快"}} {
		freq := f
		frequencies = append(frequencies, petMenuEntry{id: "pet-frequency-" + string(freq.value), label: freq.label, radio: true, checked: p.config.Frequency == freq.value, action: func() { p.changeConfig(func(c *pet.Config) { c.Frequency = freq.value }) }})
	}
	entries = append(entries, petMenuEntry{id: "pet-scale", label: "角色大小", children: sizes}, petMenuEntry{id: "pet-frequency", label: "散步频率", children: frequencies}, petMenuEntry{},
		petMenuEntry{id: "pet-conversation", label: "打开完整对话", action: func() {
			if p.openConversation != nil {
				p.openConversation()
			}
		}},
		petMenuEntry{id: "pet-cancel", label: "取消当前请求", disabled: !p.state.CanCancel, action: func() { p.cancelVoiceFor(observed.Session, observed.Turn) }},
		petMenuEntry{id: "pet-hint", label: "单击挥手 · 双击跳跃 · 拖动移动", disabled: true},
	)
	return entries
}

func nativePetMenuItems(entries []petMenuEntry) []*mygo.MenuItem {
	var items []*mygo.MenuItem
	for _, e := range entries {
		entry := e
		if entry.label == "" {
			items = append(items, mygo.Separator())
			continue
		}
		item := &mygo.MenuItem{ID: entry.id, Label: entry.label, Disabled: entry.disabled, Checked: entry.checked}
		if entry.check {
			item.Type = mygo.MenuItemCheckbox
		}
		if entry.radio {
			item.Type = mygo.MenuItemRadio
		}
		if len(entry.children) > 0 {
			item.Submenu = nativePetMenuItems(entry.children)
		}
		if entry.action != nil {
			item.Click = func(*mygo.MenuItem, *mygo.Window) { entry.action() }
		}
		items = append(items, item)
	}
	return items
}

func (p *nativePet) trayItem() *mygo.MenuItem {
	items := nativePetMenuItems(p.menuEntries())
	for _, item := range items {
		if item.ID == "pet-cancel" {
			item.Click = func(*mygo.MenuItem, *mygo.Window) { p.cancelVoice() }
		}
	}
	return &mygo.MenuItem{ID: "pet", Label: "爱弥斯桌宠", Submenu: items}
}

func (p *nativePet) updateMenu() {
	if p.menu == nil {
		return
	}
	var update func([]petMenuEntry)
	update = func(entries []petMenuEntry) {
		for _, entry := range entries {
			if it := p.menu.ItemByID(entry.id); it != nil {
				if entry.check || entry.radio {
					it.SetChecked(entry.checked)
				}
				it.SetEnabled(!entry.disabled)
			}
			update(entry.children)
		}
	}
	update(p.menuEntries())
}

func (p *nativePet) contextMenu(m *ui.Menu) {
	// MyGo rebuilds this callback at selection time; retain the actual opening
	// entries so cancel cannot silently retarget a newer turn.
	if p.contextEntries == nil {
		p.contextEntries = p.menuEntries()
	}
	p.buildContextMenu(m, p.contextEntries)
}
func (p *nativePet) buildContextMenu(m *ui.Menu, entries []petMenuEntry) {
	for _, e := range entries {
		entry := e
		if entry.label == "" {
			m.Separator()
			continue
		}
		if len(entry.children) > 0 {
			m.Submenu(entry.label, func(child *ui.Menu) { p.buildContextMenu(child, entry.children) })
			continue
		}
		it := m.Item(entry.label).ID(entry.id).Disabled(entry.disabled)
		if entry.check {
			it.Checked(entry.checked)
		}
		if entry.radio {
			it.Radio(entry.checked)
		}
		if it.Chosen() && entry.action != nil {
			p.contextEntries = nil
			entry.action()
		}
	}
}
