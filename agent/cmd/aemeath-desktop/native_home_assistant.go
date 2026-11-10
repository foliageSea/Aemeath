package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"aemeath/agent/internal/hamcp"
	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

type nativeHAState struct {
	config         hamcp.Config
	checked        bool
	status         hamcp.Status
	tools          []hamcp.Tool
	checkedAt      time.Time
	runtimeAtCheck hamcp.Status
}

func nativeHAConfig(cfg voice.Config) hamcp.Config {
	endpoint := strings.TrimSpace(cfg.HAMCPURL)
	if normalized, err := hamcp.NormalizeEndpoint(endpoint); err == nil {
		endpoint = normalized
	}
	return hamcp.Config{Enabled: cfg.HAMCPEnabled, Endpoint: endpoint, Token: strings.TrimSpace(cfg.HAAccessToken)}
}

func probeHomeAssistant(ctx context.Context, cfg hamcp.Config) ([]hamcp.Tool, error) {
	cfg.Enabled = true
	client, err := hamcp.New(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.Discover(ctx)
}

func (a *nativeUI) checkHomeAssistant() {
	if a.busy["ha-probe"] {
		return
	}
	if a.update == nil {
		a.notify("窗口尚未就绪，请稍后重试。")
		return
	}
	cfg := nativeHAConfig(a.draft.Voice)
	probe := a.haProbe
	if probe == nil {
		probe = probeHomeAssistant
	}
	dispatch := a.update
	a.busy["ha-probe"] = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tools, err := probe(ctx, cfg)
		state := nativeHAState{config: cfg, checked: true, checkedAt: time.Now(), tools: tools, status: hamcp.Status{Connected: err == nil, ToolCount: len(tools)}}
		if err != nil {
			state.tools = nil
			state.status.ToolCount = 0
			state.status.Error = hamcp.ErrorMessage(err)
		}
		dispatch(func() {
			a.busy["ha-probe"] = false
			if cfg == nativeHAConfig(a.draft.Voice) {
				state.runtimeAtCheck = a.snapshot.Voice.HomeAssistant
				a.homeAssistant = state
			}
		})
	}()
}

func (a *nativeUI) homeAssistantSettingsView(c *ui.Context) {
	t, cfg := c.Theme(), &a.draft.Voice
	ui.Column(c.Key("ha-settings")).Label("智能家居设置").FillWidth().MinWidth(0).Padding(18).Gap(14).Radius(14).Background(t.Surface).Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, "Home Assistant").FontSize(17).Bold()
				ui.Text(c, "官方 MCP Server").FontSize(11).TextColor(t.TextMuted)
			})
			if ui.Switch(c.Key("ha-enabled"), &cfg.HAMCPEnabled).Label("启用 HA MCP").Changed() {
				a.edited()
			}
		})
		a.voiceField(c, "ha-url", "HA MCP 服务地址", "http://homeassistant.local:8123/api/mcp/assist", &cfg.HAMCPURL, false)
		a.voiceField(c, "ha-access-token", "HA 长期访问令牌", "输入 Home Assistant 长期访问令牌", &cfg.HAAccessToken, true)
		if cfg.HAMCPEnabled && !cfg.LLMTools {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Agent 工具尚未启用").FontSize(11).TextColor(t.Warning).Grow(1).MinWidth(0).Wrap()
				ui.Button(c.Key("ha-enable-tools"), "开启 Agent 工具").FontSize(11).OnClick(func() { cfg.LLMTools = true; a.edited() })
			})
		}
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			label := "测试连接 / 刷新工具"
			if a.busy["ha-probe"] {
				label = "正在连接…"
			}
			ui.Button(c.Key("ha-probe"), label).Label("测试 HA 连接").FontSize(12).Disabled(a.busy["ha-probe"]).OnClick(a.checkHomeAssistant)
			hint := "使用所选 MCP 工具集及其权限"
			if strings.HasSuffix(nativeHAConfig(*cfg).Endpoint, "/api/mcp/assist") {
				hint = "Device control：管理 HA 中已暴露的实体"
			}
			ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0).Wrap()
		})
		a.homeAssistantStatusView(c)
	})
}

func (a *nativeUI) sidebarHomeAssistant(c *ui.Context) {
	cfg := nativeHAConfig(a.snapshot.Settings.Voice)
	if !cfg.Enabled {
		return
	}
	t := c.Theme()
	status := a.snapshot.Voice.HomeAssistant
	if a.homeAssistant.checked && a.homeAssistant.config == cfg && status == a.homeAssistant.runtimeAtCheck {
		status = a.homeAssistant.status
	}
	label, color := "HA · 待连接", t.TextMuted
	if status.Connected {
		label, color = fmt.Sprintf("HA · %d 个可用工具", status.ToolCount), t.Success
		if status.ToolCount == 0 {
			color = t.Warning
		}
	} else if status.Error != "" {
		label, color = "HA · 连接异常", t.Warning
	}
	ui.Button(c.Key("ha-sidebar-status"), "").Label("HA 连接详情").Tooltip(label).FillWidth().Shrink(0).Height(31).Padding(3, 7).Gap(9).BorderWidth(0).Radius(7).Background(color.Alpha(0.06)).AlignItems(ui.Center).Justify(ui.Start).Children(func() {
		ui.Box(c).Size(6, 6).Shrink(0).Radius(3).Background(color)
		ui.Text(c, label).FontSize(12).TextColor(color).Grow(1).MinWidth(0).SingleLine()
		ui.Icon(c, sidebarChevronRight).Size(14, 14).Shrink(0).TextColor(t.TextMuted)
	}).OnClick(func() { a.page = "设置"; a.settingsTab = 4 })
}

func (a *nativeUI) homeAssistantStatusView(c *ui.Context) {
	t := c.Theme()
	cfg := nativeHAConfig(a.draft.Voice)
	state := a.homeAssistant
	validProbe := state.checked && state.config == cfg
	runtime := a.snapshot.Voice.HomeAssistant
	validRuntime := cfg.Enabled && cfg == nativeHAConfig(a.snapshot.Settings.Voice) && (runtime.Connected || runtime.Error != "")
	status := hamcp.Status{}
	label, color := "尚未检查连接", t.TextMuted
	switch {
	case a.busy["ha-probe"]:
		label, color = "正在连接 Home Assistant…", t.Accent
	case validProbe && (!validRuntime || runtime == state.runtimeAtCheck):
		status = state.status
		if status.Connected {
			label, color = fmt.Sprintf("连接成功 · %d 个可用工具", status.ToolCount), t.Success
		} else {
			label, color = "连接失败", t.Warning
		}
	case validRuntime:
		status = runtime
		if status.Connected {
			label, color = fmt.Sprintf("最近连接可用 · %d 个工具", status.ToolCount), t.Success
		} else {
			label, color = "HA 暂不可用", t.Warning
		}
	case state.checked:
		label = "配置已更改，请重新测试连接"
	}
	if status.Connected && status.ToolCount == 0 {
		label, color = "已连接 · 0 个可用工具", t.Warning
	}
	ui.Column(c.Key("ha-status")).Label("HA 连接状态").FillWidth().MinWidth(0).Padding(12).Gap(8).Radius(9).Border(1, color.Alpha(0.3)).Background(color.Alpha(0.04)).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(color).Wrap()
		if status.Error != "" {
			ui.Text(c, status.Error).FontSize(11).TextColor(t.TextMuted).Wrap().Selectable()
		}
		if status.Connected && status.ToolCount == 0 {
			ui.Text(c, "尚无可用工具，暂不能通过 HA 控制设备。请检查 MCP 工具集配置及权限。").FontSize(11).TextColor(t.Warning).Wrap()
		}
		if validProbe && (!validRuntime || runtime == state.runtimeAtCheck) && !a.busy["ha-probe"] {
			ui.Text(c, "最近检查 · "+state.checkedAt.Format("15:04:05")).FontSize(10).TextColor(t.TextMuted)
			for i, tool := range state.tools {
				title := tool.Title
				if strings.TrimSpace(title) == "" {
					title = tool.Name
				}
				ui.Column(c.Key(fmt.Sprintf("ha-discovered-%d", i))).FillWidth().MinWidth(0).Gap(3).Padding(5, 0).Children(func() {
					ui.Text(c, title).FontSize(12).MaxLines(1).Ellipsis("…").Tooltip(title)
					ui.Text(c, tool.Name).FontSize(10).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
				})
			}
		}
	})
}
