package main

import (
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aemeath/agent/internal/netproxy"
	"github.com/egoist/mygo/ui"
)

func TestNativeNetworkModesDefaultAndManualDraft(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(1100, 1100)
	nativeClick(t, tt, "设置")
	if radio, ok := tt.AccessByRole(ui.RoleRadio, "系统"); !ok || !radio.Checked {
		t.Fatal("empty network settings must select system mode")
	}
	if a.dirty || a.revision != 0 || a.draft.Network.Mode != "" {
		t.Fatal("rendering legacy defaults changed the draft")
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "代理 URL"); ok {
		t.Fatal("system mode shows manual URL")
	}
	if !tt.HasText("网络代理") {
		t.Fatal("missing network settings title")
	}
	for _, text := range []string{"优先使用环境变量", "PAC / WPAD", "重新连接语音会话", "Home Assistant 始终直连"} {
		if tt.HasText(text) {
			t.Fatalf("network settings contain explanatory text %q", text)
		}
	}
	nativeClick(t, tt, "代理")
	if a.draft.Network.Mode != netproxy.ModeProxy || !a.dirty || a.revision != 1 {
		t.Fatal("proxy mode did not mark the draft edited")
	}
	field := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "代理 URL")
	if !field.Password {
		t.Fatal("manual proxy URL is not masked")
	}
	for _, text := range []string{"http / https / socks5", "不会回退直连", "优先使用环境变量", "PAC / WPAD", "重新连接语音会话", "Home Assistant 始终直连"} {
		if tt.HasText(text) {
			t.Fatalf("manual proxy settings contain explanatory text %q", text)
		}
	}
	nativeClick(t, tt, "代理 URL")
	url := "socks5://user:password@127.0.0.1:7890"
	tt.Type(url)
	if a.draft.Network.ProxyURL != url || a.revision <= 1 {
		t.Fatal("URL edit did not update draft/revision")
	}
	field = nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "代理 URL")
	if strings.Contains(field.Value, "password") {
		t.Fatal("masked accessibility value exposes proxy credentials")
	}
	revision := a.revision
	nativeClick(t, tt, "显示 代理 URL")
	field = nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "代理 URL")
	if field.Password || field.Value != url || a.revision != revision {
		t.Fatal("reveal lost URL or marked settings edited")
	}
	nativeClick(t, tt, "隐藏 代理 URL")
	for _, tc := range []struct{ label, mode string }{{"直连", netproxy.ModeDirect}, {"系统", netproxy.ModeSystem}} {
		radio := nativeSettingsFormTestAccess(t, tt, ui.RoleRadio, tc.label)
		tt.ClickAt(radio.Bounds.X+radio.Bounds.W/2, radio.Bounds.Y+radio.Bounds.H/2)
		if selected, ok := tt.AccessByRole(ui.RoleRadio, tc.label); !ok || !selected.Checked {
			t.Fatalf("mode %s was not selected", tc.label)
		}
		if a.draft.Network.Mode != tc.mode || a.draft.Network.ProxyURL != url {
			t.Fatalf("%s: config=%+v, want mode=%s URL=%s", tc.label, a.draft.Network, tc.mode, url)
		}
		if _, ok := tt.AccessByRole(ui.RoleTextField, "代理 URL"); ok {
			t.Fatal("inactive manual URL is still visible")
		}
	}
	a.refresh()
	nativeClick(t, tt, "通用")
	nativeClick(t, tt, "连接")
	nativeClick(t, tt, "代理")
	field = nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "代理 URL")
	if !field.Password || a.draft.Network.ProxyURL != url || len(f.saves) != 0 {
		t.Fatal("navigation/refresh lost unsaved manual proxy or saved it early")
	}
}

func TestNativeNetworkSavePreservesNewerDraftAndFailure(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 1100)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "代理")
	nativeClick(t, tt, "代理 URL")
	tt.Type("http://127.0.0.1:7890")
	want := a.draft.Network
	gate := make(chan struct{})
	f.gate = gate
	nativeClick(t, tt, "保存设置")
	nativeClick(t, tt, "直连")
	close(gate)
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Network != want {
		t.Fatal("save did not capture the submitted network config")
	}
	if !a.dirty || a.draft.Network.Mode != netproxy.ModeDirect || a.draft.Network.ProxyURL != want.ProxyURL {
		t.Fatal("save completion discarded newer mode or inactive URL")
	}
	f.saveError = errors.New("invalid proxy configuration")
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if !a.dirty || len(f.saves) != 1 || !strings.Contains(a.notice, "invalid proxy configuration") {
		t.Fatal("failed save discarded draft or hid error")
	}
	f.saveError = nil
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if a.dirty || len(f.saves) != 2 || f.saves[1].Network != a.draft.Network {
		t.Fatal("current network draft did not save")
	}
}

// Opt-in visual review using the real settings view, without launching the app.
func TestNativeNetworkPreview(t *testing.T) {
	dir := os.Getenv("AEMEATH_SETTINGS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set AEMEATH_SETTINGS_PREVIEW_DIR to export the network proxy preview")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a, _, _, tt := nativeTest(t)
	tt.SetSize(1100, 800)
	a.draft.Network.Mode = netproxy.ModeProxy
	nativeClick(t, tt, "设置")
	field := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "代理 URL")
	if !field.Password {
		t.Fatal("proxy preview must use the default masked input")
	}
	nativeSettingsFormTestInside(t, "proxy preview input", field.Bounds, 0, 0, 1100, 800)
	f, err := os.Create(filepath.Join(dir, "network-proxy-1100x800.png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, tt.Image())
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestNativeNetworkCardResponsiveLayout(t *testing.T) {
	for _, width := range []int{320, 431, 432, 640} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/dark=%t", width, dark), func(t *testing.T) {
				a := &nativeUI{}
				a.draft.Network = netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: "https://user:password@127.0.0.1:7890"}
				tt := ui.NewTester(func(c *ui.Context) {
					nativeSettingsBody(c, "body", func() { a.networkSettingsView(c) })
				}, width, 700)
				tt.SetDark(dark)
				group := nativeSettingsFormTestAccess(t, tt, ui.RoleRadioGroup, "代理模式")
				field := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "代理 URL")
				button := nativeSettingsFormTestAccess(t, tt, ui.RoleButton, "显示 代理 URL")
				for name, bounds := range map[string]ui.Rect{"mode": group.Bounds, "URL": field.Bounds, "reveal": button.Bounds} {
					nativeSettingsFormTestInside(t, name, bounds, 20, 0, float32(width)-20, 700)
				}
				if field.Bounds.X+field.Bounds.W > button.Bounds.X || field.Bounds.Y >= button.Bounds.Y+button.Bounds.H {
					t.Fatal("proxy URL overlaps reveal control")
				}
				if field.Bounds.Y < group.Bounds.Y+group.Bounds.H {
					t.Fatal("URL overlaps mode selector")
				}
			})
		}
	}
}
