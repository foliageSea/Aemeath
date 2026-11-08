package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/proto"
	"github.com/egoist/mygo/ui"
)

type nativeFake struct {
	mu        sync.Mutex
	state     desktop.State
	saves     []desktop.Settings
	pages     []string
	screens   [][2]int // [on (0/1), brightness]
	saveError error
	gate      <-chan struct{}
}

func (f *nativeFake) Snapshot() desktop.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.state
	s.Settings = copySettings(s.Settings)
	return s
}
func (f *nativeFake) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Running = true
	return nil
}
func (f *nativeFake) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Running = false
	return nil
}
func (f *nativeFake) SaveSettings(s desktop.Settings) error {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveError != nil {
		return f.saveError
	}
	f.saves = append(f.saves, copySettings(s))
	f.state.Settings = copySettings(s)
	return nil
}
func (f *nativeFake) SetPage(page string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages = append(f.pages, page)
	f.state.Page = page
	return nil
}
func (f *nativeFake) SetScreen(on bool, brightness int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	onInt := 0
	if on {
		onInt = 1
	}
	f.screens = append(f.screens, [2]int{onInt, brightness})
	f.state.ScreenOn, f.state.Brightness = on, brightness
	return nil
}
func (f *nativeFake) RefreshQuotas() error { return nil }
func nativeTest(t *testing.T) (*nativeUI, *nativeFake, chan func(), *ui.Tester) {
	t.Helper()
	f := &nativeFake{state: desktop.State{Settings: desktop.Settings{Address: "saved.local", MagpieURL: "http://localhost:3425", Subscriptions: []desktop.Subscription{{Name: "A"}, {Name: "B"}, {Name: "C"}}}, Page: "metrics", ScreenOn: true, Brightness: 80, Status: "stopped", Quota: &proto.QuotaFrame{TS: time.Now().Unix(), Accounts: []proto.QuotaAccount{{Name: "A"}, {Name: "B"}, {Name: "C"}}}}}
	a := nativeState(f)
	a.loginRead = func() bool { return false }
	a.loginWrite = func(bool) error { return nil }
	updates := make(chan func(), 10)
	a.update = func(fn func()) { updates <- fn }
	tt := ui.NewTester(a.view, 1100, 900)
	return a, f, updates, tt
}
func nativeComplete(t *testing.T, updates chan func(), tt *ui.Tester) {
	t.Helper()
	select {
	case fn := <-updates:
		fn()
		tt.Frame()
	case <-time.After(2 * time.Second):
		t.Fatal("async action did not dispatch completion")
	}
}
func nativeClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func TestNativeSettingsDraftSurvivesRefreshAndPendingSave(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "设备地址")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("first.local")
	if a.draft.Address != "first.local" || !a.dirty {
		t.Fatalf("input not bound: %+v", a.draft)
	}
	a.refresh()
	tt.Frame()
	if a.draft.Address != "first.local" || !tt.Focused("设备地址") {
		t.Fatal("refresh overwrote editor or focus")
	}
	gate := make(chan struct{})
	f.gate = gate
	nativeClick(t, tt, "保存设置")
	nativeClick(t, tt, "设备地址")
	tt.Key(0, ui.KeyEnd)
	tt.Type("-new")
	if !a.busy["save"] {
		t.Fatal("save did not run asynchronously")
	}
	close(gate)
	nativeComplete(t, updates, tt)
	if !a.dirty || a.draft.Address != "first.local-new" {
		t.Fatal("save completion discarded edits made while saving")
	}
	f.mu.Lock()
	saved := f.saves[0].Address
	f.mu.Unlock()
	if saved != "first.local" {
		t.Fatal("save did not capture immutable draft", saved)
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if a.dirty {
		t.Fatal("successful current draft save remains dirty")
	}
}

func TestNativeSubscriptionPersistenceAndStableFocus(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	a.draft.Address = "unfinished.local"
	a.edited()
	nativeClick(t, tt, "订阅")
	quotaPointerDrag(t, tt, "C", "B", true)
	nativeComplete(t, updates, tt)
	if !tt.Focused("拖动 C") {
		t.Fatal("reorder moved focus away from subscription identity")
	}
	if got := a.subscriptions(); got[1].Name != "C" {
		t.Fatal(got)
	}
	quotaPointerDrag(t, tt, "C", "A", true)
	nativeComplete(t, updates, tt)
	if a.subscriptions()[0].Name != "C" {
		t.Fatal("second move did not act on same subscription")
	}
	nativeClick(t, tt, "隐藏 B")
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	saved := copySettings(f.state.Settings)
	f.mu.Unlock()
	if saved.Address != "saved.local" || !saved.Subscriptions[2].Hidden {
		t.Fatalf("subscription save touched connection draft or lost hidden state: %+v", saved)
	}
	if a.draft.Address != "unfinished.local" || !a.dirty {
		t.Fatal("subscription update discarded unsaved connection draft")
	}
	nativeClick(t, tt, "恢复显示 B")
	nativeComplete(t, updates, tt)
	if a.subscriptions()[2].Hidden {
		t.Fatal("restore did not clear hidden preference")
	}
}

func TestNativeOfflineControlsServiceAndPage(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	nativeClick(t, tt, "额度屏")
	if len(f.pages) != 0 {
		t.Fatal("offline page control sent command")
	}
	nativeClick(t, tt, "启动服务")
	nativeComplete(t, updates, tt)
	if !a.snapshot.Running {
		t.Fatal("native start failed")
	}
	nativeClick(t, tt, "停止服务")
	nativeComplete(t, updates, tt)
	if a.snapshot.Running {
		t.Fatal("native stop failed")
	}
	f.mu.Lock()
	f.state.Connected = true
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "额度屏")
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pages) != 1 || f.pages[0] != "quota" {
		t.Fatal("page control sent incorrect command", f.pages)
	}
}

func TestNativeScreenControl(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	nativeClick(t, tt, "息屏")
	if len(f.screens) != 0 {
		t.Fatal("offline screen control sent command")
	}
	f.mu.Lock()
	f.state.Connected = true
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "息屏")
	if len(f.screens) != 0 {
		t.Fatal("screen control acted before the device reported its state")
	}
	f.mu.Lock()
	f.state.ScreenKnown = true
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	// Header quick toggle: on -> off, label flips to the opposite action.
	nativeClick(t, tt, "息屏")
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	if len(f.screens) != 1 || f.screens[0] != [2]int{0, 80} {
		f.mu.Unlock()
		t.Fatalf("header toggle sent incorrect command: %+v", f.screens)
	}
	f.mu.Unlock()
	if a.snapshot.ScreenOn || !tt.HasText("亮屏") {
		t.Fatal("header toggle did not turn the screen off")
	}
	// Flip back on so the header label ("息屏") no longer collides with the
	// settings switch's static "亮屏" label once the settings page is open.
	nativeClick(t, tt, "亮屏")
	nativeComplete(t, updates, tt)
	if !a.snapshot.ScreenOn {
		t.Fatal("header toggle did not turn the screen back on")
	}
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "亮屏") // the screen-on switch, labeled statically like the login switch
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.screens) != 3 || f.screens[2] != [2]int{0, 80} {
		t.Fatalf("settings switch sent incorrect command: %+v", f.screens)
	}
}

func TestNativeFailedSavePreservesDraftAndCanRetry(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	a.draft.MagpieURL = "bad-url"
	a.edited()
	f.saveError = errors.New("invalid gateway")
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if !a.dirty || a.draft.MagpieURL != "bad-url" || !strings.Contains(a.notice, "invalid gateway") {
		t.Fatal("save error discarded input or was hidden")
	}
	f.saveError = nil
	a.draft.MagpieURL = "http://other:3425"
	a.edited()
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if a.dirty {
		t.Fatal("retry failed")
	}
}

func TestNativeFailedSubscriptionSaveCanRetry(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	f.saveError = errors.New("disk full")
	nativeClick(t, tt, "订阅")
	nativeClick(t, tt, "隐藏 B")
	nativeComplete(t, updates, tt)
	if !a.subscriptions()[1].Hidden || !strings.Contains(a.notice, "disk full") {
		t.Fatal("failed subscription change lost draft or error")
	}
	a.refresh()
	tt.Frame()
	if !a.subscriptions()[1].Hidden {
		t.Fatal("refresh discarded failed preference draft")
	}
	f.saveError = nil
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.state.Settings.Subscriptions[1].Hidden {
		t.Fatal("explicit retry failed to persist hidden preference")
	}
}

func TestNativeMetricsAndErrorsRender(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.mu.Lock()
	f.state.Status = "reconnecting"
	f.state.Error = "device unavailable"
	f.state.LastSent = 1700000000
	f.state.Metrics = &proto.Frame{Host: "test-host", OS: "darwin", TS: time.Now().Unix(), CPU: &proto.CPU{Name: "test-cpu", Total: proto.F(25)}, Mem: &proto.Mem{Used: 1 << 30, Total: 4 << 30}}
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "连接异常 · 查看详情")
	for _, text := range []string{"等待重新连接", "设备连接：device unavailable", "CPU · test-cpu", "内存 · 1.0 GB / 4.0 GB"} {
		if !tt.HasText(text) {
			t.Errorf("missing rendered text %q: %v", text, tt.Texts())
		}
	}
}

func TestNativeLoginIsAsynchronousAndDispatched(t *testing.T) {
	a, _, updates, tt := nativeTest(t)
	enabled := false
	a.loginRead = func() bool { return enabled }
	started := make(chan bool, 1)
	release := make(chan struct{})
	a.loginWrite = func(value bool) error { started <- value; <-release; return nil }
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "登录系统时启动")
	select {
	case value := <-started:
		if !value {
			t.Fatal("login switch did not request true")
		}
	case <-time.After(time.Second):
		t.Fatal("login write did not start")
	}
	if !a.busy["login"] {
		t.Fatal("login request blocked UI instead of remaining pending")
	}
	nativeClick(t, tt, "概览") // Other navigation stays interactive while OS API waits.
	enabled = true
	close(release)
	nativeComplete(t, updates, tt)
	if !a.login || a.busy["login"] {
		t.Fatal("login completion did not update UI state")
	}
}

func TestNativeMetricHistoryUsesNewRealSamplesOnly(t *testing.T) {
	a, f, _, _ := nativeTest(t)
	a.refresh()
	if len(a.history) != 0 {
		t.Fatal("missing metrics created fabricated history")
	}
	f.state.Metrics = &proto.Frame{TS: 100, CPU: &proto.CPU{Total: proto.F(25)}}
	a.refresh()
	a.refresh()
	if len(a.history) != 1 || a.history[0].values[0] != 25 || !math.IsNaN(a.history[0].values[2]) {
		t.Fatal("duplicate TS or missing sensor recorded as fake sample")
	}
	*f.state.Metrics.CPU.Total = 99
	if a.history[0].values[0] != 25 {
		t.Fatal("history aliases latest metric pointers")
	}
	for i := 101; i <= 165; i++ {
		f.state.Metrics = &proto.Frame{TS: int64(i), CPU: &proto.CPU{Total: proto.F(float64(i))}}
		a.refresh()
	}
	if len(a.history) != 60 || a.history[0].ts != 106 || a.history[59].ts != 165 {
		t.Fatal("history did not retain latest 60 real samples")
	}
	f.state.Metrics = &proto.Frame{TS: 164}
	a.refresh()
	if len(a.history) != 60 || a.history[59].ts != 165 {
		t.Fatal("out-of-order metric changed history")
	}
}

func TestNativeResponsiveMetricCardsStayWithinWindow(t *testing.T) {
	for _, width := range []int{760, 1100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a, _, _, _ := nativeTest(t)
			tt := ui.NewTester(a.view, width, 800)
			cpu, ok := tt.Find("指标卡 CPU")
			if !ok {
				t.Fatal("missing CPU card")
			}
			memory, ok := tt.Find("指标卡 内存")
			if !ok {
				t.Fatal("missing memory card")
			}
			gpu, ok := tt.Find("指标卡 GPU")
			if !ok {
				t.Fatal("missing GPU card")
			}
			if math.Abs(float64(cpu.Y-memory.Y)) > 1 {
				t.Fatal("first two cards do not share a row")
			}
			if width == 1100 && math.Abs(float64(cpu.Y-gpu.Y)) > 1 {
				t.Fatal("1100px view is not three columns")
			}
			if width == 760 && gpu.Y <= cpu.Y {
				t.Fatal("760px view is not two columns")
			}
			for _, name := range []string{"CPU", "内存", "GPU", "磁盘", "网络"} {
				r, ok := tt.Find("指标卡 " + name)
				if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > float32(width)+1 {
					t.Fatalf("card %s exceeds width %d: %+v", name, width, r)
				}
			}
			for _, label := range []string{"性能屏", "额度屏", "启动服务"} {
				r, ok := tt.Find(label)
				if !ok || r.X < 0 || r.X+r.W > float32(width)+1 || r.Y+r.H > 800 {
					t.Fatalf("control %s outside window: %+v", label, r)
				}
			}
		})
	}
}

func TestNativeSidebarHoverPressAndRefreshFrames(t *testing.T) {
	a, _, _, tt := nativeTest(t)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "设备地址")
	tt.Type("-editing")
	for _, target := range []string{"概览", "订阅", "设置", "概览"} {
		access, ok := tt.AccessByRole(ui.RoleButton, target)
		if !ok || access.Disabled || !access.Focusable {
			t.Fatalf("navigation access state invalid: %+v", access)
		}
		// Use the icon portion, not just the label center used by Tester.Click.
		x, y := access.Bounds.X+16, access.Bounds.Y+access.Bounds.H/2
		tt.Move(x, y)
		for i := 0; i < 3; i++ {
			a.refresh()
			tt.Frame()
			tt.Advance(20 * time.Millisecond)
		}
		tt.Press(x, y)
		for i := 0; i < 3; i++ {
			tt.Frame()
			tt.Advance(20 * time.Millisecond)
		}
		tt.Release(x, y)
		if a.page != target {
			t.Fatalf("hovered icon press navigated to %q, want %q", a.page, target)
		}
		for i := 0; i < 8; i++ {
			a.refresh()
			tt.Frame()
			tt.Advance(100 * time.Millisecond)
			if a.page != target {
				t.Fatal("refresh reverted navigation")
			}
		}
		if tt.FramePending() {
			t.Fatal("navigation keeps requesting frames after settling")
		}
		selected, ok := tt.AccessByRole(ui.RoleButton, target)
		if !ok || !(selected.Selected || selected.Checked) {
			t.Fatalf("selected navigation access state missing: %+v", selected)
		}
	}
}

func TestNativeBaseButtonChildrenHoverAndKeyboard(t *testing.T) {
	clicks := 0
	view := func(c *ui.Context) {
		b := ui.ButtonBase(c.Key("base")).Label("base navigation").Width(180).Height(32)
		if b.Hovered() {
			b.Background(c.Theme().SurfaceHover)
		}
		b.Children(func() { ui.Box(c).Size(20, 20); ui.Text(c, "Navigate") }).OnClick(func() { clicks++ })
	}
	tt := ui.NewTester(view, 300, 100)
	tt.Move(15, 15)
	tt.Frame()
	tt.Press(15, 15)
	tt.Frame()
	tt.Release(15, 15)
	if clicks != 1 {
		t.Fatalf("ButtonBase hover/children consumed pointer click: %d", clicks)
	}
	tt.Key(0, ui.KeyEnter)
	if clicks != 2 {
		t.Fatalf("ButtonBase keyboard activation failed: %d", clicks)
	}
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Fatal("ButtonBase hover caused endless frame requests")
	}
}

func TestNativeDescriptionsStayConcise(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	a.dirty = true
	a.draft.Subscriptions[0].Hidden = true
	f.state.Quota = nil
	a.refresh()
	tt.Frame()
	for _, page := range []string{"概览", "订阅", "设置"} {
		nativeClick(t, tt, page)
		for _, removed := range []string{"完整订阅列表", "前 8 个", "每账户最多", "请在设置中配置", "不发送至设备", "偏好仍已保留", "留空自动发现", "支持主机名", "不会丢失修改", "驻留托盘", "未收到屏幕确认", "最近60个采样", "本机每秒采样"} {
			if tt.HasText(removed) {
				t.Errorf("%s still shows removed explanation %q", page, removed)
			}
		}
		if page == "订阅" && (!tt.HasText("暂无额度") || !tt.HasText("已隐藏")) {
			t.Fatal("concise subscription states missing")
		}
		if page == "设置" && !tt.HasText("未保存") {
			t.Fatal("concise unsaved state missing")
		}
	}
}

func TestNativeFullSubscriptionListAndUnavailableMetrics(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.mu.Lock()
	for i := 3; i < 12; i++ {
		f.state.Quota.Accounts = append(f.state.Quota.Accounts, proto.QuotaAccount{Name: fmt.Sprintf("account-%02d", i)})
	}
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	if len(a.subscriptions()) != 12 {
		t.Fatal("UI list truncated at firmware limit")
	}
	nativeClick(t, tt, "订阅")
	if tt.HasText("前 8 个") || tt.HasText("每账户最多") {
		t.Fatal("device capacity explanation should not appear in UI")
	}
	nativeClick(t, tt, "概览")
	if !tt.HasText("等待本机首次采样…") {
		t.Fatal("missing metrics state not rendered")
	}
}
