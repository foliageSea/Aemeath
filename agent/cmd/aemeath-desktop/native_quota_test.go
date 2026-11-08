package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/proto"
	"github.com/egoist/mygo/ui"
)

func quotaPointerDrag(t *testing.T, tt *ui.Tester, source, target string, before bool) {
	t.Helper()
	handle, ok := tt.Find("拖动 " + source)
	if !ok {
		t.Fatal("missing drag handle", source)
	}
	row, ok := tt.AccessByRole(ui.RoleListItem, target)
	if !ok {
		t.Fatal("missing target row", target)
	}
	x, y := handle.X+handle.W/2, handle.Y+handle.H/2
	destination := row.Bounds.Y + 2
	if !before {
		destination = row.Bounds.Y + row.Bounds.H - 2
	}
	tt.Press(x, y)
	tt.Move(x, y-9)
	tt.Move(x, destination)
	tt.Release(x, destination)
}
func quotaNames(a *nativeUI) []string {
	var names []string
	for _, s := range a.subscriptions() {
		names = append(names, s.Name)
	}
	return names
}
func assertQuotaNames(t *testing.T, a *nativeUI, want ...string) {
	t.Helper()
	if got := quotaNames(a); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestNativeQuotaUsageCurrencyAndOriginalBalance(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.state.Quota.Usage = &proto.QuotaUsage{CallsToday: 1364, CostToday: 41.3534, Calls30d: 13958, Cost30d: 328.1980}
	f.state.Quota.Accounts[0].Balance = "余额 ¥88.50"
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "订阅")
	if !tt.HasText("今日 1364 次 / 41.3534 USD · 近30天 13958 次 / 328.1980 USD") {
		t.Fatal("usage costs missing currency or precision")
	}
	if !tt.HasText("余额 ¥88.50") || tt.HasText("¥88.50 USD") {
		t.Fatal("gateway balance currency was changed")
	}
	for _, text := range tt.Texts() {
		if text == "上移" || text == "下移" {
			t.Fatal("old reorder buttons remain")
		}
	}
}
func TestNativeQuotaPointerDragToFirstAndLast(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	nativeClick(t, tt, "订阅")
	quotaPointerDrag(t, tt, "C", "A", true)
	nativeComplete(t, updates, tt)
	assertQuotaNames(t, a, "C", "A", "B")
	quotaPointerDrag(t, tt, "C", "B", false)
	nativeComplete(t, updates, tt)
	assertQuotaNames(t, a, "A", "B", "C")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saves) != 2 {
		t.Fatal("drop did not save exactly once", len(f.saves))
	}
}
func TestNativeQuotaCanceledDragDoesNotSave(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	nativeClick(t, tt, "订阅")
	handle, _ := tt.Find("拖动 C")
	x, y := handle.X+handle.W/2, handle.Y+handle.H/2
	tt.Press(x, y)
	tt.Move(x, y-9)
	tt.Move(50, 80)
	tt.Release(50, 80)
	assertQuotaNames(t, a, "A", "B", "C")
	handle, _ = tt.Find("拖动 B")
	x, y = handle.X+handle.W/2, handle.Y+handle.H/2
	tt.Press(x, y)
	tt.Move(x, y-9)
	tt.Key(0, ui.KeyEscape)
	tt.Release(x, 200)
	assertQuotaNames(t, a, "A", "B", "C")
	if len(f.saves) != 0 || a.busy["save"] {
		t.Fatal("canceled drag persisted preferences")
	}
}
func TestNativeQuotaFailedDropRetriesOnlyPreferences(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	a.draft.Address = "unfinished.local"
	a.draft.MagpieURL = "unfinished URL"
	a.edited()
	f.saveError = errors.New("disk full")
	nativeClick(t, tt, "订阅")
	quotaPointerDrag(t, tt, "C", "A", true)
	nativeComplete(t, updates, tt)
	assertQuotaNames(t, a, "C", "A", "B")
	if !a.quotaUnsaved || !strings.Contains(a.notice, "disk full") {
		t.Fatal("failed drop lost retry state")
	}
	a.refresh()
	tt.Frame()
	assertQuotaNames(t, a, "C", "A", "B")
	f.saveError = nil
	nativeClick(t, tt, "重试保存")
	nativeComplete(t, updates, tt)
	if a.quotaUnsaved {
		t.Fatal("successful retry remains unsaved")
	}
	if _, exists := tt.Find("重试保存"); exists {
		t.Fatal("retry button remains after success")
	}
	f.mu.Lock()
	saved := copySettings(f.state.Settings)
	f.mu.Unlock()
	if saved.Address != "saved.local" || saved.MagpieURL != "http://localhost:3425" || saved.Subscriptions[0].Name != "C" {
		t.Fatalf("retry wrote connection draft or lost order: %+v", saved)
	}
	if !a.dirty || a.draft.Address != "unfinished.local" {
		t.Fatal("retry lost unfinished connection draft")
	}
}
func TestNativeQuotaHiddenUnavailableAndBusyDrag(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	a.draft.Subscriptions = []desktop.Subscription{{Name: "A"}, {Name: "unavailable", Hidden: true}, {Name: "B"}, {Name: "C"}}
	nativeClick(t, tt, "订阅")
	quotaPointerDrag(t, tt, "unavailable", "A", true)
	nativeComplete(t, updates, tt)
	assertQuotaNames(t, a, "unavailable", "A", "B", "C")
	if !a.subscriptions()[0].Hidden {
		t.Fatal("reorder lost unavailable hidden preference")
	}
	gate := make(chan struct{})
	f.gate = gate
	nativeClick(t, tt, "隐藏 B")
	if a.quotaList.Reorder == nil {
		t.Fatal("busy transition changed list structure")
	}
	quotaPointerDrag(t, tt, "C", "A", true)
	a.reorderSubscriptions([]int{3}, 0)
	nativeClick(t, tt, "隐藏 C")
	if a.subscriptions()[3].Hidden {
		t.Fatal("busy visibility action was not rejected")
	}
	assertQuotaNames(t, a, "unavailable", "A", "B", "C")
	close(gate)
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saves) != 2 || !f.state.Settings.Subscriptions[2].Hidden {
		t.Fatal("visibility click changed order or busy drag saved")
	}
}
func TestNativeQuotaRowsRefreshAfterAsyncSaveAndNewQuota(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	nativeClick(t, tt, "订阅")
	gate := make(chan struct{})
	f.gate = gate
	quotaPointerDrag(t, tt, "C", "A", true)
	for _, name := range []string{"A", "B", "C"} {
		handle, ok := tt.AccessByRole(ui.RoleButton, "拖动 "+name)
		visibility, visible := tt.AccessByRole(ui.RoleButton, "隐藏 "+name)
		if !ok || !visible || handle.Disabled || visibility.Disabled {
			t.Fatalf("pending save changed %s control structure", name)
		}
	}
	close(gate)
	nativeComplete(t, updates, tt)
	for frame := 0; frame < 3; frame++ {
		a.refresh()
		tt.Frame()
		for _, name := range []string{"A", "B", "C"} {
			handle, ok := tt.AccessByRole(ui.RoleButton, "拖动 "+name)
			visibility, visible := tt.AccessByRole(ui.RoleButton, "隐藏 "+name)
			if !ok || !visible || handle.Disabled || visibility.Disabled || !handle.Focusable || !visibility.Focusable {
				t.Fatalf("completed save left %s controls stale: %+v %+v", name, handle, visibility)
			}
		}
	}
	// Update content without changing account identities or leaving this page.
	f.mu.Lock()
	f.state.Quota.Accounts[0].Balance = "fresh balance EUR 12.34"
	f.state.Quota.Accounts[0].Windows = []proto.QuotaWindow{{Name: "fresh-window", Used: 37, Display: "fresh quota"}}
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	if !tt.HasText("fresh balance EUR 12.34") || !tt.HasText("fresh-window · 37.0% · fresh quota") {
		t.Fatal("quota rows retained content from a previous snapshot")
	}
	nativeClick(t, tt, "隐藏 B")
	nativeComplete(t, updates, tt)
	restored, ok := tt.AccessByRole(ui.RoleButton, "恢复显示 B")
	if !ok || restored.Disabled || !restored.Focusable {
		t.Fatal("hidden row action did not refresh after save")
	}
	nativeClick(t, tt, "恢复显示 B")
	nativeComplete(t, updates, tt)
	if a.subscriptions()[2].Hidden {
		t.Fatal("refreshed visibility control did not accept subsequent click")
	}
}

func TestNativeQuotaTwelveVariableRowsReachableByScrolling(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.state.Quota.Accounts = nil
	a.draft.Subscriptions = nil
	for i := 0; i < 12; i++ {
		account := proto.QuotaAccount{Name: fmt.Sprintf("account-%02d", i)}
		for j := 0; j < i%4; j++ {
			account.Windows = append(account.Windows, proto.QuotaWindow{Name: fmt.Sprintf("window-%d", j), Used: float64(j) * 10})
		}
		f.state.Quota.Accounts = append(f.state.Quota.Accounts, account)
	}
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "订阅")
	if len(a.subscriptions()) != 12 {
		t.Fatal("complete subscription list truncated")
	}
	a.quotaList.ScrollToEnd()
	tt.Frame()
	last, ok := tt.AccessByRole(ui.RoleListItem, "account-11")
	if !ok || last.Offscreen || last.Bounds.H <= 0 || last.Bounds.Y >= 900 {
		t.Fatalf("last variable row unreachable: %+v", last)
	}
	a.quotaList.ScrollTo(0, ui.Start)
	tt.Frame()
	first, ok := tt.Find("拖动 account-00")
	if !ok {
		t.Fatal("cannot scroll back to first row")
	}
	tt.Move(first.X, first.Y)
	tt.Scroll(first.X, first.Y, 0, 10000)
	tt.Advance(time.Second)
	last, ok = tt.AccessByRole(ui.RoleListItem, "account-11")
	if !ok || last.Offscreen || last.Bounds.Y >= 900 {
		t.Fatalf("pointer scroll failed to reach last row: %+v", last)
	}
}
func TestNativeQuotaDragAutoscrollToLast(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	a.draft.Subscriptions = nil
	f.state.Quota.Accounts = nil
	for i := 0; i < 12; i++ {
		f.state.Quota.Accounts = append(f.state.Quota.Accounts, proto.QuotaAccount{Name: fmt.Sprintf("scroll-%02d", i), Windows: []proto.QuotaWindow{{Name: "day", Used: 10}}})
	}
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "订阅")
	handle, ok := tt.Find("拖动 scroll-00")
	if !ok {
		t.Fatal("missing first drag handle")
	}
	viewport, ok := tt.Find("订阅列表")
	if !ok {
		t.Fatal("missing list viewport")
	}
	x, y := handle.X+handle.W/2, handle.Y+handle.H/2
	bottom := viewport.Y + viewport.H - 2
	tt.Press(x, y)
	tt.Move(x, y-9)
	tt.Move(x, bottom)
	for i := 0; i < 80 && !a.quotaList.AtEnd(); i++ {
		tt.Advance(100 * time.Millisecond)
	}
	if !a.quotaList.AtEnd() {
		tt.Key(0, ui.KeyEscape)
		tt.Release(x, bottom)
		t.Fatal("drag near list edge did not scroll to end")
	}
	tt.Release(x, bottom)
	nativeComplete(t, updates, tt)
	names := quotaNames(a)
	if names[0] != "scroll-01" || names[11] != "scroll-00" {
		t.Fatal("auto-scroll drop missed final boundary", names)
	}
}

func TestMoveSubscriptionsBoundaries(t *testing.T) {
	source := []desktop.Subscription{{Name: "A"}, {Name: "B", Hidden: true}, {Name: "C"}, {Name: "D"}}
	for _, test := range []struct {
		rows []int
		to   int
		want string
	}{{[]int{3}, 0, "DABC"}, {[]int{0}, 4, "BCDA"}, {[]int{0, 2}, 4, "BDAC"}, {[]int{1}, 2, "ABCD"}, {[]int{-1}, 0, "ABCD"}} {
		got := moveSubscriptions(source, test.rows, test.to)
		names := ""
		for _, s := range got {
			names += s.Name
			if s.Name == "B" && !s.Hidden {
				t.Fatal("hidden state lost")
			}
		}
		if names != test.want {
			t.Fatalf("move(%v,%d) = %s, want %s", test.rows, test.to, names, test.want)
		}
	}
	if source[0].Name != "A" {
		t.Fatal("move mutated input")
	}
}
