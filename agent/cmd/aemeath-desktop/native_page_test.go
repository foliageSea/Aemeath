package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func assertNativePageSelection(t *testing.T, tt *ui.Tester, selected string, disabled bool) {
	t.Helper()
	for i, label := range []string{"性能屏", "额度屏", "语音屏"} {
		info, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || (info.Selected || info.Checked) != (nativePageNames[i] == selected) || info.Disabled != disabled {
			t.Fatalf("page %s: %+v; want selected=%q disabled=%v", label, info, selected, disabled)
		}
	}
	if tt.HasText("页面未知") != (selected == "") {
		t.Fatalf("unknown page status mismatch: selected=%q, texts=%v", selected, tt.Texts())
	}
}

func TestNativePageUnknownAllowsEveryExplicitRequestWithoutACK(t *testing.T) {
	for _, initialPage := range []string{"", "metrics"} {
		t.Run(fmt.Sprintf("page=%s", initialPage), func(t *testing.T) {
			a, f, updates, tt := nativeTest(t)
			f.mu.Lock()
			f.state.Connected = true
			f.state.Page, f.state.PageKnown = initialPage, false
			f.mu.Unlock()
			a.refresh()
			tt.Frame()
			assertNativePageSelection(t, tt, "", false)
			for i, label := range []string{"性能屏", "额度屏", "语音屏"} {
				nativeClick(t, tt, label)
				assertNativePageSelection(t, tt, "", true)
				nativeComplete(t, updates, tt)
				assertNativePageSelection(t, tt, "", false)
				f.mu.Lock()
				count, page := len(f.pages), f.pages[len(f.pages)-1]
				f.mu.Unlock()
				if count != i+1 || page != nativePageNames[i] {
					t.Fatalf("request mismatch: count=%d page=%s", count, page)
				}
			}
		})
	}
}

func TestNativePageReportsDriveSelectionAndRequestsDoNotChangeIt(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	// Simulate device reports from a manual swipe, temporary voice page and restore.
	for _, page := range []string{"quota", "voice", "quota", "metrics"} {
		f.mu.Lock()
		f.state.Connected, f.state.PageKnown, f.state.Page = true, true, page
		f.mu.Unlock()
		a.refresh()
		tt.Frame()
		assertNativePageSelection(t, tt, page, false)
	}
	f.mu.Lock()
	count := len(f.pages)
	f.mu.Unlock()
	if count != 0 {
		t.Fatal("rendering reports sent a page command")
	}
	// Re-selecting the currently known page is still an explicit command.
	for i, label := range []string{"性能屏", "额度屏", "语音屏"} {
		nativeClick(t, tt, label)
		nativeComplete(t, updates, tt)
		assertNativePageSelection(t, tt, "metrics", false)
		f.mu.Lock()
		count, page := len(f.pages), f.pages[len(f.pages)-1]
		f.mu.Unlock()
		if count != i+1 || page != nativePageNames[i] {
			t.Fatalf("request mismatch: %d %s", count, page)
		}
	}
	f.mu.Lock()
	f.state.Page, f.state.PageKnown = "voice", true
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	assertNativePageSelection(t, tt, "voice", false)
}

func TestNativePageOfflineAndReconnectStayUnknownUntilReport(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	for _, connected := range []bool{false, true, false} {
		f.mu.Lock()
		f.state.Connected = connected
		f.state.Page, f.state.PageKnown = "", false
		f.mu.Unlock()
		a.refresh()
		tt.Frame()
		assertNativePageSelection(t, tt, "", !connected)
		if !connected {
			for _, label := range []string{"性能屏", "额度屏", "语音屏"} {
				nativeClick(t, tt, label)
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pages) != 0 {
		t.Fatalf("offline controls sent commands: %v", f.pages)
	}
}

func TestNativePageSelectorFitsNarrowLayout(t *testing.T) {
	for _, page := range []string{"概览", "订阅", "设置", "语音助手"} {
		for _, known := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/known=%v", page, known), func(t *testing.T) {
				a, _, _, tt := nativeTest(t)
				a.page = page
				a.snapshot.Connected, a.snapshot.PageKnown = true, known
				tt.SetSize(760, 800)
				tt.Frame()
				for _, label := range []string{"性能屏", "额度屏", "语音屏"} {
					info, ok := tt.AccessByRole(ui.RoleButton, label)
					if !ok || info.Bounds.W < 40 || info.Bounds.X < 206 || info.Bounds.X+info.Bounds.W > 760 {
						t.Fatalf("page control outside narrow window: %s %+v", label, info)
					}
				}
				if !known {
					r, ok := tt.Find("页面未知")
					if !ok || r.X < 206 || r.X+r.W > 760 {
						t.Fatalf("unknown status outside window: %+v", r)
					}
				}
			})
		}
	}
}
