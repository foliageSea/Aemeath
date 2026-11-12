package main

import "github.com/egoist/mygo/ui"

const (
	nativeSettingsPageWidth       float32 = 760
	nativeSettingsNavigationWidth float32 = 128
	nativeSettingsNavigationGap   float32 = 16
	nativeSettingsScrollbarGutter float32 = 16
)

func nativeSettingsBody(c *ui.Context, key string, content func()) {
	ui.Column(c.Key(key)).FillWidth().MinWidth(0).MaxWidth(nativeSettingsPageWidth).Padding(0, nativeSettingsScrollbarGutter, 0, 0).Gap(24).Children(content)
}

func nativeSettingsSection(c *ui.Context, key, title string, content func()) {
	ui.Column(c.Key(key)).FillWidth().MinWidth(0).Padding(20).Gap(16).Radius(14).Background(c.Theme().Surface).Children(func() {
		if title != "" {
			ui.Text(c, title).FontSize(15).Bold()
		}
		content()
	})
}

func nativeSettingsField(c *ui.Context, key, title string, control func() ui.Element) {
	ui.Row(c.Key(key)).FillWidth().MinWidth(0).Wrap().Gap(12).AlignItems(ui.Center).Children(func() {
		label := ui.Text(c, title).Width(140).Shrink(0).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		ui.Column(c.Key("control")).Basis(240).Grow(1).MinWidth(0).Children(func() {
			control().LabelledBy(label)
		})
	})
}

func nativeSettingsNumberField(c *ui.Context, key, title string, control func() ui.Element, description ...string) {
	field := ui.Field(c.Key(key), title, func() {
		control().FontSize(13).TextColor(c.Theme().Text)
	}).FillWidth().MinWidth(0).FontSize(12).TextColor(c.Theme().TextMuted).Gap(8).AlignItems(ui.Stretch)
	if len(description) > 0 && description[0] != "" {
		field.DescribedBy(ui.Text(c.Key(key+"-description"), description[0]).Label(description[0]).Hidden(true))
	}
	width, _ := c.Size()
	if width >= 900 {
		field.Grid().ColumnTracks(ui.Fixed(140), ui.Fr(1)).Gap(12).AlignItems(ui.Center)
	}
}

func nativeSettingsToggle(c *ui.Context, key, title string, value *bool, disabled bool, controlTitle ...string) bool {
	changed := false
	name := title
	if len(controlTitle) > 0 {
		name = controlTitle[0]
	}
	ui.Row(c.Key(key + "-row")).FillWidth().MinWidth(0).MinHeight(36).Gap(16).AlignItems(ui.Center).Children(func() {
		label := ui.Text(c, title).Label(name).Grow(1).MinWidth(0).FontSize(13).Wrap()
		changed = ui.Switch(c.Key(key), value).Label(name).LabelledBy(label).Shrink(0).Disabled(disabled).Changed()
	})
	return changed
}

func nativeSettingsTextArea(c *ui.Context, key, title string, control func() ui.Element) {
	ui.Column(c.Key(key)).FillWidth().MinWidth(0).Gap(8).Children(func() {
		label := ui.Text(c, title).FontSize(12).TextColor(c.Theme().TextMuted)
		control().LabelledBy(label)
	})
}

// Size a segmented choice by its labels and the theme’s segment padding,
// rather than stretching it across the settings form or fixing a pixel width.
func nativeSettingsSegmented(c *ui.Context, key string, selected *int, title string, labels ...string) ui.Element {
	t := c.Theme()
	width := 2 + t.Space(1) // track border and horizontal padding
	for _, label := range labels {
		textWidth, _ := c.MeasureText(0, ui.Span{Text: label})
		width += max(44, textWidth+2*t.Space(3)) // each segment stays easy to click
	}
	width += float32(max(0, len(labels)-1)) * t.Space(0.5)
	return ui.Segmented(c.Key(key), selected, labels...).Label(title).Width(width).Shrink(0).Height(36)
}

func nativeSettingsActions(c *ui.Context, key string, content func()) {
	ui.Row(c.Key(key)).FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(content)
}
