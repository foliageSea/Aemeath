package main

import (
	"context"
	"strings"
	"time"

	"aemeath/agent/internal/brain"

	"github.com/egoist/mygo/ui"
)

type nativeModelSource struct {
	baseURL string
	apiKey  string
}

type nativeRemoteModels struct {
	source nativeModelSource
	models []string
	open   bool
	query  string
	scroll ui.ScrollState
	fetch  func(context.Context, string, string) ([]string, error)
}

func (a *nativeUI) modelSource() nativeModelSource {
	baseURL := strings.TrimRight(strings.TrimSpace(a.draft.Voice.LLMBaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return nativeModelSource{baseURL: baseURL, apiKey: strings.TrimSpace(a.draft.Voice.LLMAPIKey)}
}

func (a *nativeUI) fetchRemoteModels() {
	if a.busy["remote-models"] {
		return
	}
	if a.update == nil {
		a.notify(nativeNoticeWarning, "窗口尚未就绪，请稍后重试。")
		return
	}
	source := a.modelSource()
	fetch := a.remoteModels.fetch
	if fetch == nil {
		fetch = brain.FetchRemoteModels
	}
	parent := context.Background()
	if service, ok := a.service.(nativeNetworkService); ok {
		parent = service.NetworkContext(parent)
	}
	dispatch := a.update
	a.busy["remote-models"] = true
	a.remoteModels.models = nil
	a.remoteModels.open = false
	go func() {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		models, err := fetch(ctx, source.baseURL, source.apiKey)
		dispatch(func() {
			a.busy["remote-models"] = false
			if source != a.modelSource() {
				return
			}
			if err != nil {
				a.notify(nativeNoticeError, "获取模型失败："+err.Error())
				return
			}
			if len(models) == 0 {
				a.notify(nativeNoticeWarning, "未获取到可用模型")
				return
			}
			a.remoteModels.source = source
			a.remoteModels.models = append([]string(nil), models...)
			a.remoteModels.query = ""
			a.remoteModels.scroll = ui.ScrollState{}
			a.remoteModels.open = true
		})
	}()
}

func (a *nativeUI) voiceModelSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	nativeSettingsField(c, "voice-llm-model", "对话模型", func() ui.Element {
		var field ui.Element
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			field = ui.TextInput(c.Key("input"), &cfg.LLMModel).Label("对话模型").Placeholder("填写服务提供的模型名称").Basis(160).Grow(1).MinWidth(0).Height(36)
			if field.Changed() {
				a.edited()
			}
			label := "获取模型"
			if a.busy["remote-models"] {
				label = "正在获取…"
			}
			ui.Button(c.Key("fetch"), label).Label("获取模型").Height(36).Shrink(0).Disabled(a.busy["remote-models"]).OnClick(a.fetchRemoteModels)
		})
		return field
	})
	a.remoteModelsDialogView(c)
}

func (a *nativeUI) remoteModelsDialogView(c *ui.Context) {
	s := &a.remoteModels
	if s.source != a.modelSource() {
		s.open = false
	}
	width, height := c.Size()
	ui.Modal(c.Key("remote-models-dialog"), &s.open, func() {
		ui.Column(c.Key("body")).FillWidth().MinWidth(0).Gap(12).Children(func() {
			ui.Text(c, "选择对话模型").Bold().FontSize(16)
			if ui.SearchField(c.Key("search"), &s.query).Label("搜索模型").Placeholder("搜索模型").AutoFocus().FillWidth().MinWidth(0).Height(36).Changed() {
				s.scroll = ui.ScrollState{}
			}
			ui.Scroll(c.Key("models")).Label("远程模型列表").FillWidth().MinWidth(0).Height(min(360, max(80, height-260))).TrackScroll(&s.scroll).Children(func() {
				ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
					query := strings.ToLower(strings.TrimSpace(s.query))
					count := 0
					for _, model := range s.models {
						if !strings.Contains(strings.ToLower(model), query) {
							continue
						}
						count++
						selected := model == a.draft.Voice.LLMModel
						button := ui.Button(c.Key(model), "").Label(model).AccessSelected(selected).FillWidth().MinWidth(0).Padding(10, 12).Justify(ui.Start)
						if selected {
							button.Background(c.Theme().Accent.Alpha(0.12))
						}
						button.Children(func() { ui.Text(c, model).MinWidth(0).Wrap() }).OnClick(func() {
							if s.source == a.modelSource() && a.draft.Voice.LLMModel != model {
								a.draft.Voice.LLMModel = model
								a.edited()
							}
							s.open = false
						})
					}
					if count == 0 {
						ui.Text(c, "无匹配模型").FontSize(12).TextColor(c.Theme().TextMuted)
					}
				})
			})
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				ui.Button(c.Key("cancel"), "取消").OnClick(func() { s.open = false })
			})
		})
	}).Label("选择对话模型").Width(min(560, max(0, width-48)))
}
