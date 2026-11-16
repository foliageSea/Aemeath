package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNativeAppearanceSettings(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, width := range []int{760, 1100} {
			t.Run(fmt.Sprintf("dark=%v/width=%d", dark, width), func(t *testing.T) {
				a, f, _, tt := nativeTest(t)
				tt.SetSize(width, 600)
				tt.SetDark(dark)
				tt.SetVibrancy(true)
				var sources []mygo.ThemeSource
				a.themeWrite = func(source mygo.ThemeSource) { sources = append(sources, source) }
				nativeClick(t, tt, "设置")
				nativeClick(t, tt, "通用")
				assertSelected := func(selected string) {
					t.Helper()
					for _, label := range []string{"跟随系统", "亮色", "暗色"} {
						info, ok := tt.AccessByRole(ui.RoleRadio, label)
						if !ok || info.Checked != (label == selected) {
							t.Fatalf("appearance %s selection mismatch: %+v", label, info)
						}
						if info.Bounds.X < 0 || info.Bounds.X+info.Bounds.W > float32(width) {
							t.Fatalf("appearance option outside window: %+v", info)
						}
					}
				}
				assertSelected("跟随系统")
				for _, choice := range []struct {
					label  string
					source mygo.ThemeSource
					dark   bool
				}{
					{"暗色", mygo.ThemeDark, true}, {"亮色", mygo.ThemeLight, false},
				} {
					nativeClick(t, tt, choice.label)
					assertSelected(choice.label)
					if a.theme == nil || a.theme.Dark != choice.dark || sources[len(sources)-1] != choice.source {
						t.Fatalf("settings did not synchronize appearance: %s", choice.label)
					}
				}
				nativeClick(t, tt, "切换到暗色模式")
				assertSelected("暗色")
				nativeClick(t, tt, "跟随系统")
				assertSelected("跟随系统")
				if a.theme != nil || sources[len(sources)-1] != mygo.ThemeSystem {
					t.Fatal("follow system did not clear UI and native overrides")
				}
				for _, systemDark := range []bool{false, true, false} {
					tt.SetDark(systemDark)
					label := "切换到暗色模式"
					if systemDark {
						label = "切换到亮色模式"
					}
					if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
						t.Fatal("appearance no longer follows system changes")
					}
					assertSelected("跟随系统")
				}
				a.refresh()
				nativeClick(t, tt, "概览")
				nativeClick(t, tt, "设置")
				assertSelected("跟随系统")
				if a.dirty || len(f.saves) != 0 {
					t.Fatal("appearance choices modified unrelated settings")
				}
			})
		}
	}
}
