// Aemeath is the MyGO desktop companion and tray host for the Agent.
package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"time"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/holdkey"
	"github.com/egoist/mygo"
)

func main() {
	mygo.App.SetName("Aemeath")
	var service *desktop.Service
	// Generation must have no side effects, including single-instance locks.
	if os.Getenv("MYGO_GENERATE") != "" {
		if err := mygo.App.Run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	var win *mygo.Window
	var quitting bool
	var cancel context.CancelFunc
	var voiceRefreshDone, slowRefreshDone <-chan struct{}
	var cancelVoiceAssets func()
	var stopVoiceHotkey func()
	show := func() {
		if win != nil {
			win.Show()
			win.Restore()
			win.Focus()
		}
	}
	mygo.App.OnSecondInstance(func([]string, string) { show() })
	mygo.App.OnActivate(func(visible bool) {
		if !visible {
			show()
		}
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		quitting = true
		if stopVoiceHotkey != nil {
			stopVoiceHotkey()
		}
		if cancelVoiceAssets != nil {
			cancelVoiceAssets()
		}
		if cancel != nil {
			cancel()
		}
	})
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		dir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		if logs, err := mygo.App.Path(mygo.PathLogs); err == nil {
			if f, err := os.OpenFile(filepath.Join(logs, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
				log.SetOutput(f)
			}
		}
		service, err = desktop.New(filepath.Join(dir, "settings.json"))
		if err != nil {
			log.Fatal(err)
		}
		if err := service.Start(); err != nil {
			log.Printf("start: %v", err)
		}
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
		native := newNativeUI(service)
		cancelVoiceAssets = native.cancelWakeAssets
		win = mygo.NewWindow(nativeWindowOptions(native.view, mygo.App.WasOpenedAtLogin()))
		native.update = win.Update
		stopVoiceHotkey = native.installGlobalVoiceHold(holdkey.Start, holdkey.StartAlternate)
		if native.voiceGlobalError != "" {
			log.Print(native.voiceGlobalError)
		}
		win.OnClose(func(e *mygo.CloseEvent) {
			if !quitting {
				e.PreventDefault()
				win.Hide()
			}
		})
		report := func(action func() error) {
			go func() {
				if err := action(); err != nil {
					log.Printf("tray: %v", err)
				}
			}()
		}
		menu := mygo.NewMenu([]*mygo.MenuItem{
			{Label: "打开 Aemeath", Click: func(*mygo.MenuItem, *mygo.Window) { show() }},
			{ID: "status", Label: "正在启动…", Disabled: true},
			mygo.Separator(),
			{ID: "start", Label: "启动服务", Disabled: true, Click: func(*mygo.MenuItem, *mygo.Window) { report(service.Start) }},
			{ID: "stop", Label: "暂停服务", Click: func(*mygo.MenuItem, *mygo.Window) { report(service.Stop) }},
			mygo.Separator(),
			{ID: "metrics", Label: "监控台 · 性能", Disabled: true, Click: func(*mygo.MenuItem, *mygo.Window) { report(func() error { return service.SetPage("metrics") }) }},
			{ID: "quota", Label: "监控台 · 订阅额度", Disabled: true, Click: func(*mygo.MenuItem, *mygo.Window) { report(func() error { return service.SetPage("quota") }) }},
			{ID: "voice", Label: "监控台 · 语音助手", Disabled: true, Click: func(*mygo.MenuItem, *mygo.Window) { report(func() error { return service.SetPage("voice") }) }},
			mygo.Separator(),
			{ID: "screen-on", Label: "点亮屏幕", Disabled: true, Click: func(*mygo.MenuItem, *mygo.Window) {
				report(func() error { return service.SetScreen(true, service.Snapshot().Brightness) })
			}},
			{ID: "screen-off", Label: "熄灭屏幕", Disabled: true, Click: func(*mygo.MenuItem, *mygo.Window) {
				report(func() error { return service.SetScreen(false, service.Snapshot().Brightness) })
			}},
			mygo.Separator(),
			{Label: "退出 Aemeath", Role: mygo.RoleQuit},
		})
		ctx, stop := context.WithCancel(context.Background())
		cancel = stop
		refreshed := make(chan struct{})
		voiceRefreshDone = refreshed
		go func() {
			defer close(refreshed)
			runVoiceRefresh(ctx, service.VoiceUpdates(), win.Update, native.refreshVoice)
		}()
		tray, err := mygo.NewTray(mygo.TrayOptions{ID: "aemeath-agent", Icon: trayPNG(), IconIsTemplate: true, ToolTip: "Aemeath", Menu: menu})
		if err != nil {
			log.Printf("tray: %v", err)
			mygo.App.SetActivationPolicy(mygo.ActivationPolicyRegular)
			show()
		}
		slowDone := make(chan struct{})
		slowRefreshDone = slowDone
		go func() {
			defer close(slowDone)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				win.Update(func() {
					if ctx.Err() == nil {
						native.refresh()
					}
				})
				if ctx.Err() != nil {
					return
				}
				if tray == nil {
					continue
				}
				s := service.Snapshot()
				label := "已暂停"
				if s.Running {
					label = "正在连接监控台"
				}
				if s.Connected {
					label = "已连接 · " + s.Target
				}
				tray.SetToolTip("Aemeath · " + label)
				menu.ItemByID("status").SetLabel(label)
				menu.ItemByID("start").SetEnabled(!s.Running)
				menu.ItemByID("stop").SetEnabled(s.Running)
				menu.ItemByID("metrics").SetEnabled(s.Connected)
				menu.ItemByID("quota").SetEnabled(s.Connected)
				menu.ItemByID("voice").SetEnabled(s.Connected)
				menu.ItemByID("screen-on").SetEnabled(s.Connected && s.ScreenKnown && !s.ScreenOn)
				menu.ItemByID("screen-off").SetEnabled(s.Connected && s.ScreenKnown && s.ScreenOn)
			}
		}()
	})
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "文件", Submenu: []*mygo.MenuItem{{Label: "关闭窗口", Accelerator: "CmdOrCtrl+W", Click: func(*mygo.MenuItem, *mygo.Window) {
			if win != nil {
				win.Close()
			}
		}}}},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleWindowMenu},
	}))
	if err := mygo.App.Run(); err != nil {
		log.Print(err)
	}
	if cancel != nil {
		cancel()
	}
	if voiceRefreshDone != nil {
		<-voiceRefreshDone
	}
	if slowRefreshDone != nil {
		<-slowRefreshDone
	}
	if service != nil {
		service.Close()
	}
}

// A template pulse icon, rendered at Retina size without external assets.
func trayPNG() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 36, 36))
	points := [][2]int{{3, 19}, {10, 19}, {14, 8}, {20, 28}, {25, 15}, {29, 19}, {33, 19}}
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		steps := max(abs(b[0]-a[0]), abs(b[1]-a[1]))
		for n := 0; n <= steps; n++ {
			x, y := a[0]+(b[0]-a[0])*n/steps, a[1]+(b[1]-a[1])*n/steps
			for dx := -1; dx <= 1; dx++ {
				for dy := -1; dy <= 1; dy++ {
					img.SetNRGBA(x+dx, y+dy, color.NRGBA{A: 255})
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(fmt.Sprintf("tray icon: %v", err))
	}
	return buf.Bytes()
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
