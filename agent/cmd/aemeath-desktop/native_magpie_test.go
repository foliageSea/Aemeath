package main

import (
	"errors"
	"testing"

	"aemeath/agent/internal/desktop"
	"github.com/egoist/mygo/ui"
)

func TestNativeMagpieDefaultOff(t *testing.T) {
	a := nativeState(&nativeFake{state: desktop.State{}})
	if a.draft.MagpieEnabled || a.snapshot.Settings.MagpieEnabled {
		t.Fatal("quota monitoring must be opt-in")
	}
}

func TestNativeMagpieSwitchSavedAndDraftPreserved(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	nativeClick(t, tt, "设置")
	toggle := nativeSettingsFormTestAccess(t, tt, ui.RoleSwitch, "额度监控")
	tt.ClickAt(toggle.Bounds.X+toggle.Bounds.W/2, toggle.Bounds.Y+toggle.Bounds.H/2)
	if a.draft.MagpieEnabled || !a.dirty || !a.snapshot.Settings.MagpieEnabled || len(f.saves) != 0 {
		t.Fatal("switch must update only the unsaved draft")
	}
	if field := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Magpie 网关"); !field.Disabled {
		t.Fatal("gateway field must be disabled with monitoring")
	}
	a.refresh()
	tt.Frame()
	if a.draft.MagpieEnabled {
		t.Fatal("refresh overwrote monitoring draft")
	}
	f.saveError = errors.New("disk full")
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if !a.dirty || a.draft.MagpieEnabled || !a.snapshot.Settings.MagpieEnabled {
		t.Fatal("failed save changed active setting or lost draft")
	}
	f.saveError = nil
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if a.dirty || a.snapshot.Settings.MagpieEnabled || len(f.saves) != 1 {
		t.Fatal("monitoring switch was not saved")
	}
	if a.draft.MagpieURL != "http://localhost:3425" || len(a.draft.Subscriptions) != 3 {
		t.Fatal("disable lost gateway or subscription preferences")
	}
	nativeClick(t, tt, "订阅")
	if !tt.HasText("额度监控已关闭") || tt.HasText("刷新额度") {
		t.Fatal("disabled page must show status without refresh action")
	}
	if _, ok := tt.AccessByRole(ui.RoleListItem, "A"); ok {
		t.Fatal("disabled page displayed cached accounts")
	}
	nativeClick(t, tt, "前往连接设置")
	if a.page != "设置" || a.settingsTab != nativeSettingsConnectionTab {
		t.Fatal("settings shortcut did not navigate")
	}
	toggle = nativeSettingsFormTestAccess(t, tt, ui.RoleSwitch, "额度监控")
	tt.ClickAt(toggle.Bounds.X+toggle.Bounds.W/2, toggle.Bounds.Y+toggle.Bounds.H/2)
	if !a.draft.MagpieEnabled {
		t.Fatal("cannot re-enable monitoring")
	}
	if field := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Magpie 网关"); field.Disabled {
		t.Fatal("enabled gateway field remains disabled")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	nativeClick(t, tt, "订阅")
	if tt.HasText("额度监控已关闭") || !tt.HasText("刷新额度") {
		t.Fatal("enabled page did not restore refresh")
	}
}
