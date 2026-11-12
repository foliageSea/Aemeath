package main

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The layout helpers must also work without the app sidebar, navigation, or
// scrolling. Keep these fixtures small so a clipped app viewport cannot hide an
// overflowing control or accidentally make an alignment assertion pass.
func nativeSettingsFormTestFields(width int) (*ui.Tester, *[2]string) {
	values := &[2]string{"first.local", "second.local"}
	tt := ui.NewTester(func(c *ui.Context) {
		nativeSettingsBody(c, "form", func() {
			nativeSettingsSection(c, "section", "", func() {
				for i, title := range []string{"Short", "A longer field label"} {
					nativeSettingsField(c, fmt.Sprintf("field-%d", i), title, func() ui.Element {
						return ui.TextInput(c.Key("input"), &values[i]).Label(title).FillWidth().MinWidth(0).Height(36)
					})
				}
			})
		})
	}, width, 500)
	return tt, values
}

func nativeSettingsFormTestAccess(t *testing.T, tt *ui.Tester, role ui.Role, name string) ui.AccessInfo {
	t.Helper()
	info, ok := tt.AccessByRole(role, name)
	if !ok || info.Bounds.W <= 0 || info.Bounds.H <= 0 {
		t.Fatalf("missing or empty control %q: %+v", name, info)
	}
	return info
}

func nativeSettingsFormTestNear(t *testing.T, what string, got, want float32) {
	t.Helper()
	if math.Abs(float64(got-want)) > 0.5 {
		t.Errorf("%s = %.2f, want %.2f", what, got, want)
	}
}

func nativeSettingsFormTestInside(t *testing.T, what string, r ui.Rect, left, top, right, bottom float32) {
	t.Helper()
	if r.W <= 0 || r.H <= 0 || r.X < left-0.5 || r.Y < top-0.5 || r.X+r.W > right+0.5 || r.Y+r.H > bottom+0.5 {
		t.Errorf("%s outside [%.1f, %.1f, %.1f, %.1f]: %+v", what, left, top, right, bottom, r)
	}
}

func TestNativeSettingsFormResponsiveFields(t *testing.T) {
	// The form reserves 16 DIPs for the scrollbar; the section uses 40
	// DIPs of horizontal padding. Label, gap and control fit at 448 DIPs.
	for _, width := range []int{640, 320, 447, 448, 449} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			tt, _ := nativeSettingsFormTestFields(width)
			var startX float32
			for i, title := range []string{"Short", "A longer field label"} {
				label, ok := tt.Find(title)
				if !ok || label.W <= 0 || label.H <= 0 {
					t.Fatalf("missing visible label %q: %+v", title, label)
				}
				field := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, title).Bounds
				if width >= 448 {
					if label.X+label.W > field.X || label.Y >= field.Y+field.H || field.Y >= label.Y+label.H {
						t.Errorf("wide label/control must share a row: label=%+v field=%+v", label, field)
					}
				} else {
					if field.Y < label.Y+label.H {
						t.Errorf("narrow label must be above its control: label=%+v field=%+v", label, field)
					}
					nativeSettingsFormTestNear(t, "stacked label/control left edge", field.X, label.X)
				}
				if i == 0 {
					startX = field.X
				} else {
					nativeSettingsFormTestNear(t, "field input starting edge", field.X, startX)
				}
				nativeSettingsFormTestInside(t, title, field, 20, 0, float32(width)-20-nativeSettingsScrollbarGutter, 500)
			}
		})
	}
}

func TestNativeSettingsFormResizePreservesValueAndFocus(t *testing.T) {
	tt, values := nativeSettingsFormTestFields(640)
	input := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Short").Bounds
	tt.ClickAt(input.X+input.W/2, input.Y+input.H/2)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("edited.local")
	for _, width := range []int{320, 640} {
		tt.SetSize(width, 500)
		info := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Short")
		if values[0] != "edited.local" || info.Value != "edited.local" || !info.Focused || !tt.Focused("Short") {
			t.Fatalf("resize to %d lost value/focus: values=%v info=%+v", width, values, info)
		}
		if values[1] != "second.local" {
			t.Fatalf("resize to %d changed another field: %v", width, values)
		}
	}
	restored := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Short").Bounds
	if restored != input {
		t.Errorf("wide layout not restored: before=%+v after=%+v", input, restored)
	}
	// Typing without another click proves that keyboard focus really survived.
	tt.Type("!")
	if values[0] != "edited.local!" {
		t.Fatalf("restored field did not retain its caret: %q", values[0])
	}
}

func TestNativeSettingsFormLabelsFocusInputs(t *testing.T) {
	for _, width := range []int{640, 320} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			tt, values := nativeSettingsFormTestFields(width)
			for i, title := range []string{"Short", "A longer field label"} {
				label, ok := tt.Find(title)
				if !ok {
					t.Fatalf("missing label %q", title)
				}
				// Click the caption's actual rectangle, not the input with the
				// same accessible name, to exercise LabelledBy activation.
				tt.ClickAt(label.X+label.W/2, label.Y+label.H/2)
				info := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, title)
				if !info.Focused {
					t.Fatalf("label %q did not focus its input: %+v", title, info)
				}
				tt.Key(ui.Cmd, ui.KeyA)
				tt.Type("from label")
				if values[i] != "from label" {
					t.Fatalf("label %q focused the wrong input: %v", title, values)
				}
			}
		})
	}
}

func TestNativeSettingsFormMaxWidthAndToggleAlignment(t *testing.T) {
	for _, width := range []int{320, 640, 1200} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			enabled := false
			tt := ui.NewTester(func(c *ui.Context) {
				nativeSettingsBody(c, "form", func() {
					ui.Row(c).Label("form width probe").FillWidth().Height(1)
					nativeSettingsSection(c, "section", "", func() {
						nativeSettingsToggle(c, "toggle", "Enable feature", &enabled, false)
					})
				})
			}, width, 300)
			form, ok := tt.Find("form width probe")
			if !ok {
				t.Fatal("missing form width probe")
			}
			wantWidth := float32(min(width, 760)) - nativeSettingsScrollbarGutter
			nativeSettingsFormTestNear(t, "form maximum width", form.W, wantWidth)
			toggle := nativeSettingsFormTestAccess(t, tt, ui.RoleSwitch, "Enable feature").Bounds
			nativeSettingsFormTestNear(t, "switch right edge", toggle.X+toggle.W, form.X+form.W-20)
			label, ok := tt.Find("Enable feature")
			if !ok || label.X+label.W > toggle.X || label.Y >= toggle.Y+toggle.H || toggle.Y >= label.Y+label.H {
				t.Errorf("toggle must remain to the right of its label: label=%+v switch=%+v", label, toggle)
			}
			tt.ClickAt(toggle.X+toggle.W/2, toggle.Y+toggle.H/2)
			if !enabled {
				t.Fatal("right-aligned switch is not clickable")
			}
		})
	}
}

func TestNativeSettingsSegmentedAdaptsToLabelWidth(t *testing.T) {
	for _, width := range []int{640, 1100} {
		for _, dark := range []bool{false, true} {
			short, long := 0, 0
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Gap(8).Children(func() {
					nativeSettingsSegmented(c, "short", &short, "Short choice", "MiMo", "Fish")
					nativeSettingsSegmented(c, "long", &long, "Long choice", "MiMo", "Low latency")
				})
			}, width, 240)
			tt.SetDark(dark)
			narrow := nativeSettingsFormTestAccess(t, tt, ui.RoleRadioGroup, "Short choice").Bounds
			wide := nativeSettingsFormTestAccess(t, tt, ui.RoleRadioGroup, "Long choice").Bounds
			if narrow.W >= 160 || wide.W <= narrow.W || wide.X != narrow.X || wide.X+wide.W > float32(width) {
				t.Fatalf("selector did not size to its labels: width=%d dark=%v short=%+v long=%+v", width, dark, narrow, wide)
			}
		}
	}
}

func TestNativeSettingsFormSecretInputBounds(t *testing.T) {
	for _, width := range []int{640, 320} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := &nativeUI{}
			secret := strings.Repeat("secret-", 30)
			tt := ui.NewTester(func(c *ui.Context) {
				nativeSettingsBody(c, "form", func() {
					nativeSettingsSection(c, "section", "", func() {
						a.voiceField(c, "secret", "API key", "", &secret, true)
					})
				})
			}, width, 300)
			for _, visible := range []bool{false, true} {
				action := "显示 API key"
				if visible {
					action = "隐藏 API key"
				}
				field := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "API key")
				button := nativeSettingsFormTestAccess(t, tt, ui.RoleButton, action)
				nativeSettingsFormTestInside(t, "secret input", field.Bounds, 20, 0, float32(width)-20, 300)
				nativeSettingsFormTestInside(t, "visibility button", button.Bounds, 20, 0, float32(width)-20, 300)
				if field.Bounds.X+field.Bounds.W > button.Bounds.X || field.Bounds.Y >= button.Bounds.Y+button.Bounds.H || button.Bounds.Y >= field.Bounds.Y+field.Bounds.H {
					t.Errorf("secret input/button overlap or occupy different rows: input=%+v button=%+v", field.Bounds, button.Bounds)
				}
				if field.Password == visible {
					t.Errorf("password state=%v when visible=%v", field.Password, visible)
				}
				tt.ClickAt(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
				if secret != strings.Repeat("secret-", 30) {
					t.Fatal("show/hide changed secret value")
				}
			}
		})
	}
}

// NumberInput contains its editable text field and step buttons inside a row.
// Exercise the real composite control: labelling only the outer row would leave
// the editable TextField unnamed and break caption activation.
func TestNativeSettingsFormNumberField(t *testing.T) {
	value := float64(12)
	ordinary := "ordinary"
	tt := ui.NewTester(func(c *ui.Context) {
		nativeSettingsBody(c, "form", func() {
			nativeSettingsSection(c, "section", "", func() {
				nativeSettingsField(c, "ordinary", "Ordinary input", func() ui.Element {
					return ui.TextInput(c.Key("input"), &ordinary).Label("Ordinary input").FillWidth().MinWidth(0).Height(36)
				})
				nativeSettingsNumberField(c, "number", "Count", func() ui.Element {
					return ui.NumberInput(c.Key("input"), &value, 0, 100, 1).Label("Count").FillWidth().MinWidth(0).Height(36)
				})
			})
		})
	}, 1100, 500)

	// Resize the same tree through both layouts and their exact viewport
	// boundary; the input's accessible label and activation must survive.
	for iteration, width := range []int{1100, 760, 899, 900, 1100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			tt.SetSize(width, 500)
			field := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Count")
			label, ok := tt.Find("Count")
			if !ok || label.W <= 0 || label.H <= 0 {
				t.Fatalf("number field lost its visible caption: %+v", label)
			}
			if width >= 900 {
				other := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Ordinary input")
				nativeSettingsFormTestNear(t, "number and ordinary input starting edge", field.Bounds.X, other.Bounds.X)
				if label.X+label.W > field.Bounds.X || label.Y >= field.Bounds.Y+field.Bounds.H || field.Bounds.Y >= label.Y+label.H {
					t.Errorf("wide number caption/control must share a row: caption=%+v input=%+v", label, field.Bounds)
				}
			} else {
				if field.Bounds.Y < label.Y+label.H {
					t.Errorf("narrow number caption must be above input: caption=%+v input=%+v", label, field.Bounds)
				}
				nativeSettingsFormTestNear(t, "narrow number caption left edge", label.X, 20)
				nativeSettingsFormTestNear(t, "narrow number input left edge", field.Bounds.X, label.X)
			}
			nativeSettingsFormTestInside(t, "number input", field.Bounds, 20, 0, 740, 500)
			wantValue := "37"
			if iteration == 0 {
				wantValue = "12"
			}
			if field.Value != wantValue || (iteration > 0 && value != 37) {
				t.Errorf("resize to %d lost number value: got %q want %q", width, field.Value, wantValue)
			}

			tt.ClickAt(label.X+label.W/2, label.Y+label.H/2)
			if focused := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Count"); !focused.Focused {
				t.Fatalf("number caption did not focus the editable input: %+v", focused)
			}
			tt.Key(ui.Cmd, ui.KeyA)
			tt.Type("37")
			if value != 37 {
				t.Fatalf("typing after caption activation did not update the number: %v", value)
			}
			for _, step := range []struct {
				name string
				want float64
			}{{"Increase", 38}, {"Decrease", 37}} {
				button := nativeSettingsFormTestAccess(t, tt, ui.RoleButton, step.name)
				nativeSettingsFormTestInside(t, step.name, button.Bounds, 20, 0, 740, 500)
				tt.ClickAt(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
				if value != step.want {
					t.Fatalf("%s after resize to %d: got %v want %v", step.name, width, value, step.want)
				}
			}
		})
	}
}

func TestNativeSettingsFormNumberDescriptionStaysAccessibleWithoutVisibleText(t *testing.T) {
	for _, width := range []int{760, 1100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			value := float64(12)
			description := "Numeric input help"
			tt := ui.NewTester(func(c *ui.Context) {
				nativeSettingsBody(c, "form", func() {
					nativeSettingsNumberField(c, "number", "Count", func() ui.Element {
						return ui.NumberInput(c, &value, 0, 100, 1).FillWidth()
					}, description)
				})
			}, width, 300)
			input := nativeSettingsFormTestAccess(t, tt, ui.RoleTextField, "Count")
			if input.Description != description {
				t.Fatalf("number input lost its accessible help: %+v", input)
			}
			if tt.HasText(description) {
				t.Fatal("accessible help must not add visible explanatory text")
			}
		})
	}
}

func TestNativeSettingsFormSaveAlignsWithContentOnResize(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%v", dark), func(t *testing.T) {
			a, _, _, tt := nativeTest(t)
			tt.SetDark(dark)
			nativeClick(t, tt, "设置")
			for _, width := range []int{760, 1100, 1920, 2560, 760} {
				tt.SetSize(width, 937)
				var sharedSaveBounds ui.Rect
				for tabIndex, tab := range []struct {
					title, control string
					role           ui.Role
				}{
					{"连接", "Magpie 网关", ui.RoleTextField},
					{"通用", "登录系统时启动", ui.RoleSwitch},
					{"屏幕", "亮度", ui.RoleSlider},
					{"助手", "角色设定", ui.RoleTextField},
					{"语音", "MiMo 服务地址", ui.RoleTextField},
					{"智能家居", "HA MCP 服务地址", ui.RoleTextField},
				} {
					nativeClick(t, tt, tab.title)
					for _, state := range []struct{ dirty, saving bool }{{false, false}, {true, false}, {true, true}} {
						a.dirty, a.busy["save"] = state.dirty, state.saving
						tt.Frame()
						control := nativeSettingsFormTestAccess(t, tt, tab.role, tab.control).Bounds
						save := nativeSettingsFormTestAccess(t, tt, ui.RoleButton, "保存设置")
						if tabIndex == 0 {
							sharedSaveBounds = save.Bounds
						} else if save.Bounds != sharedSaveBounds {
							t.Fatalf("save moved after switching tab at width %d: tab=%s got=%+v want=%+v", width, tab.title, save.Bounds, sharedSaveBounds)
						}
						expectedRight := float32(226) + min(float32(width)-246, nativeSettingsPageWidth) - nativeSettingsScrollbarGutter
						nativeSettingsFormTestNear(t, fmt.Sprintf("%s/%d shared page right edge", tab.title, width), control.X+control.W+20, expectedRight)
						nativeSettingsFormTestNear(t, fmt.Sprintf("%s/%d save and form right edge", tab.title, width), save.Bounds.X+save.Bounds.W, control.X+control.W+20)
						nativeSettingsFormTestInside(t, "save button", save.Bounds, 206, 0, float32(width)-20, 937)
						if save.Disabled != state.saving {
							t.Fatalf("save busy state lost: %+v", save)
						}
						lastTab := nativeSettingsFormTestAccess(t, tt, ui.RoleTab, "智能家居").Bounds
						if lastTab.X+lastTab.W > save.Bounds.X {
							t.Fatalf("save overlaps settings tabs: tab=%+v save=%+v", lastTab, save.Bounds)
						}
					}
					a.busy["save"] = false
				}
			}
		})
	}
}

func TestNativeSettingsFormSaveWorksAtMaximizedSize(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1920, 937)
	nativeClick(t, tt, "设置")
	nativeVoiceEdit(t, tt, "设备地址", "maximized.local")
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if a.dirty || len(f.saves) != 1 || f.saves[0].Address != "maximized.local" {
		t.Fatal("maximized save button did not save the draft")
	}
}

func TestNativeSettingsFormToolbarDoesNotScroll(t *testing.T) {
	a, _, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "助手")
	nativeClick(t, tt, "对话服务")
	before := nativeSettingsFormTestAccess(t, tt, ui.RoleButton, "保存设置").Bounds
	content, ok := tt.Find("对话服务设置内容")
	if !ok || content.W <= 0 || content.H <= 0 {
		t.Fatalf("missing scrollable content: %+v", content)
	}
	for _, dy := range []float32{4000, -4000} {
		tt.Scroll(content.X+content.W/2, content.Y+content.H/2, 0, dy)
		if dy > 0 && a.assistantSettingsScroll[1].Y <= 0 {
			t.Fatal("test did not actually scroll content")
		}
		if dy < 0 && a.assistantSettingsScroll[1].Y != 0 {
			t.Fatalf("content did not return to top: %+v", a.assistantSettingsScroll[1])
		}
		after := nativeSettingsFormTestAccess(t, tt, ui.RoleButton, "保存设置").Bounds
		if after != before {
			t.Errorf("save toolbar moved while content scrolled: before=%+v after=%+v", before, after)
		}
	}
}

// Opt-in visual artifacts for manual review; ordinary form tests never write
// files or depend on a real application, device, network, or model installation.
func TestNativeSettingsFormPreviews(t *testing.T) {
	dir := os.Getenv("AEMEATH_SETTINGS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set AEMEATH_SETTINGS_PREVIEW_DIR to export settings previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{760, 600}, {1100, 800}, {1440, 900}, {1920, 937}} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/dark=%v", size[0], size[1], dark), func(t *testing.T) {
				a, _, _, tt := nativeTest(t)
				tt.SetSize(size[0], size[1])
				tt.SetDark(dark)
				nativeClick(t, tt, "设置")
				capture := func(name string) {
					t.Helper()
					path := filepath.Join(dir, fmt.Sprintf("settings-%dx%d-dark-%v-%s.png", size[0], size[1], dark, name))
					f, err := os.Create(path)
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
				capture("connection")
				nativeClick(t, tt, "助手")
				nativeClick(t, tt, "对话服务")
				capture("conversation")
				nativeClick(t, tt, "语音")
				nativeClick(t, tt, "语音合成")
				nativeClick(t, tt, "Fish")
				capture("fish")
				nativeClick(t, tt, "声音试听")
				capture("sound")
				nativeClick(t, tt, "离线唤醒")
				nativeClick(t, tt, "模型目录与高级选项")
				capture("wake")
				content, ok := tt.Find("离线唤醒设置内容")
				if !ok {
					t.Fatal("missing wake settings scroll area")
				}
				tt.Scroll(content.X+content.W/2, content.Y+content.H/2, 0, 4000)
				capture("wake-bottom")
				// Assert that previews retain the same production section state.
				if a.voiceSettingsSection != 2 {
					t.Fatal("wake preview lost selected category")
				}
			})
		}
	}
}
