package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"aemeath/agent/internal/brain"

	"github.com/egoist/mygo/ui"
)

type nativeModelCatalogService interface {
	ModelCatalogOptions(context.Context) ([]brain.ModelCatalogProvider, error)
}

type nativeModelCatalogPicker struct {
	open       bool
	query      string
	loading    bool
	error      string
	generation uint64
	providers  []brain.ModelCatalogProvider
	list       ui.ListState
	load       func(context.Context) ([]brain.ModelCatalogProvider, error)
}

type nativeCatalogChoice struct {
	provider, model string
}

func (a *nativeUI) openModelCatalog() {
	s := &a.modelCatalogPicker
	s.open = true
	s.query = ""
	s.list = ui.ListState{}
	a.loadModelCatalog()
}

func (a *nativeUI) loadModelCatalog() {
	s := &a.modelCatalogPicker
	if a.update == nil {
		s.error = "窗口尚未就绪，请稍后重试。"
		return
	}
	load := s.load
	if load == nil {
		if service, ok := a.service.(nativeModelCatalogService); ok {
			load = service.ModelCatalogOptions
		} else {
			load = func(context.Context) ([]brain.ModelCatalogProvider, error) {
				return nil, errors.New("models.dev 目录暂不可用")
			}
		}
	}
	s.generation++
	generation := s.generation
	s.loading, s.error = true, ""
	s.providers = nil
	dispatch := a.update
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		providers, err := load(ctx)
		dispatch(func() {
			if s.generation != generation {
				return
			}
			s.loading = false
			if !s.open {
				return
			}
			if err != nil {
				s.error = "获取 models.dev 目录失败"
				return
			}
			s.providers = providers
		})
	}()
}

func (a *nativeUI) selectCatalogModel(provider, model string) {
	cfg := &a.draft.Voice
	if cfg.ModelsDevProvider != provider || cfg.ModelsDevModel != model {
		cfg.ModelsDevProvider, cfg.ModelsDevModel = provider, model
		a.edited()
	}
	a.modelCatalogPicker.open = false
}

func nativeCatalogChoices(providers []brain.ModelCatalogProvider, query string) []nativeCatalogChoice {
	terms := strings.Fields(strings.ToLower(query))
	var choices []nativeCatalogChoice
	for _, provider := range providers {
		for _, model := range provider.Models {
			text := strings.ToLower(provider.ID + " / " + model)
			match := true
			for _, term := range terms {
				if !strings.Contains(text, term) {
					match = false
					break
				}
			}
			if match {
				choices = append(choices, nativeCatalogChoice{provider.ID, model})
			}
		}
	}
	return choices
}

func (a *nativeUI) modelCatalogSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	nativeSettingsField(c, "voice-models-dev-selection", "models.dev 模型", func() ui.Element {
		value := "自动匹配"
		if cfg.ModelsDevProvider != "" || cfg.ModelsDevModel != "" {
			value = cfg.ModelsDevProvider + " / " + cfg.ModelsDevModel
		}
		return ui.Button(c.Key("choose"), "").Label("models.dev 模型").Tooltip(value).FillWidth().MinWidth(0).Height(36).Justify(ui.Start).Children(func() {
			ui.Text(c, value).Grow(1).MinWidth(0).SingleLine()
			ui.Text(c, "选择").FontSize(12).TextColor(c.Theme().TextMuted)
		}).OnClick(a.openModelCatalog)
	})
	a.modelCatalogDialogView(c)
}

func (a *nativeUI) modelCatalogDialogView(c *ui.Context) {
	s := &a.modelCatalogPicker
	width, height := c.Size()
	ui.Modal(c.Key("model-catalog-dialog"), &s.open, func() {
		ui.Column(c.Key("body")).FillWidth().MinWidth(0).Gap(12).Children(func() {
			ui.Text(c, "选择 models.dev 模型").Bold().FontSize(16)
			if ui.SearchField(c.Key("search"), &s.query).Label("搜索服务商或模型").Placeholder("搜索服务商或模型").AutoFocus().FillWidth().MinWidth(0).Height(36).Changed() {
				s.list = ui.ListState{}
			}
			switch {
			case s.loading:
				ui.Text(c, "正在获取…").FontSize(12)
			case s.error != "":
				ui.Text(c, s.error).FontSize(12).TextColor(c.Theme().Warning).Wrap()
				ui.Button(c.Key("retry"), "重试").OnClick(a.loadModelCatalog)
			default:
				choices := nativeCatalogChoices(s.providers, s.query)
				if len(choices) == 0 {
					ui.Text(c, "无匹配模型").FontSize(12).TextColor(c.Theme().TextMuted)
				} else {
					s.list.Key = func(i int) any { return choices[i] }
					s.list.Label = func(i int) string { return "目录条目 " + choices[i].provider + " / " + choices[i].model }
					ui.List(c.Key("models"), &s.list, len(choices)).Label("models.dev 模型列表").FillWidth().MinWidth(0).Height(min(360, max(80, height-260))).Gap(4).Rows(func(row ui.ListRow) {
						choice, c := choices[row.Index], row.Context
						selected := choice.provider == a.draft.Voice.ModelsDevProvider && choice.model == a.draft.Voice.ModelsDevModel
						button := ui.Button(c, "").Label(choice.provider+" / "+choice.model).AccessSelected(selected).FillWidth().MinWidth(0).Height(54).Padding(8, 12).Justify(ui.Start)
						if selected {
							button.Background(c.Theme().Accent.Alpha(0.12))
						}
						button.Children(func() {
							ui.Column(c).FillWidth().MinWidth(0).Gap(3).Children(func() {
								ui.Text(c, choice.model).FontSize(13).MinWidth(0).SingleLine()
								ui.Text(c, choice.provider).FontSize(11).TextColor(c.Theme().TextMuted).SingleLine()
							})
						}).OnClick(func() { a.selectCatalogModel(choice.provider, choice.model) })
					})
				}
			}
			ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c.Key("automatic"), "自动匹配").OnClick(func() { a.selectCatalogModel("", "") })
				ui.Button(c.Key("cancel"), "取消").OnClick(func() { s.open = false })
			})
		})
	}).Label("选择 models.dev 模型").Width(min(600, max(0, width-48)))
}
