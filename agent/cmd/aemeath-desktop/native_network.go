package main

import (
	"aemeath/agent/internal/netproxy"
	"github.com/egoist/mygo/ui"
)

func (a *nativeUI) networkSettingsView(c *ui.Context) {
	nativeSettingsSection(c, "network-card", "网络代理", func() {
		cfg := &a.draft.Network
		nativeSettingsField(c, "network-mode-field", "代理模式", func() ui.Element {
			mode := 0 // Empty legacy settings use system mode without changing the draft.
			switch cfg.Mode {
			case netproxy.ModeProxy:
				mode = 1
			case netproxy.ModeDirect:
				mode = 2
			}
			field := nativeSettingsSegmented(c, "network-mode", &mode, "代理模式", "系统", "代理", "直连")
			if field.Changed() {
				cfg.Mode = []string{netproxy.ModeSystem, netproxy.ModeProxy, netproxy.ModeDirect}[mode]
				// Keep the inactive URL so switching back restores the manual draft.
				a.edited()
			}
			return field
		})
		if cfg.Mode == netproxy.ModeProxy {
			a.voiceField(c, "network-proxy-url", "代理 URL", "http://127.0.0.1:7890", &cfg.ProxyURL, true)
		}
	})
}
