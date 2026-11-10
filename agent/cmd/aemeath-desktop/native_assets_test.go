package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aemeath/agent/internal/wake"
	"github.com/egoist/mygo/ui"
)

func nativeFinishWake(t *testing.T, a *nativeUI, updates chan func(), tt *ui.Tester) {
	t.Helper()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for a.busy["wake-assets"] {
		select {
		case fn := <-updates:
			fn()
			tt.Frame()
		case <-timeout.C:
			t.Fatal("asset task did not complete")
		}
	}
}

func TestNativeWakeInitialCheckNeverInstalls(t *testing.T) {
	a, _, updates, tt := nativeTest(t)
	a.wakeAssets.initialCheck = false
	checks := make(chan string, 1)
	a.wakeAssetOps.verify = func(path string) error { checks <- path; return os.ErrNotExist }
	a.wakeAssetOps.install = func(context.Context, string, func(wake.InstallProgress)) error {
		t.Error("initial view downloaded models")
		return nil
	}
	tt.SetSize(1100, 2600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "离线唤醒")
	nativeFinishWake(t, a, updates, tt)
	if path := <-checks; path != wake.DefaultModelDir() {
		t.Fatalf("check path=%s", path)
	}
	if !tt.HasText("未安装") {
		t.Fatal("missing models have no setup state")
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "唤醒模型目录"); ok {
		t.Fatal("advanced model path exposed by default")
	}
	nativeClick(t, tt, "模型目录与高级选项")
	if _, ok := tt.AccessByRole(ui.RoleTextField, "唤醒模型目录"); !ok {
		t.Fatal("advanced model path unavailable")
	}
}

func TestNativeWakeInstallProgressAndDraftSurviveNavigation(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 2600)
	target := filepath.Join(t.TempDir(), "custom-models")
	a.draft.Voice.WakeModelDir = target
	a.draft.Voice.LLMModel = "unfinished-model"
	a.edited()
	entered, release := make(chan string, 1), make(chan struct{})
	a.wakeAssetOps.install = func(ctx context.Context, path string, report func(wake.InstallProgress)) error {
		report(wake.InstallProgress{Stage: "downloading-model", Downloaded: 512, Total: 1024})
		entered <- path
		<-release
		return nil
	}
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "离线唤醒")
	nativeClick(t, tt, "安装唤醒模型")
	select {
	case path := <-entered:
		if path != target {
			t.Fatal(path)
		}
	case <-time.After(time.Second):
		t.Fatal("installer blocked UI")
	}
	nativeComplete(t, updates, tt)
	if !tt.HasText("正在下载唤醒模型") || !a.busy["wake-assets"] {
		t.Fatal("missing live download progress")
	}
	nativeClick(t, tt, "模型目录与高级选项")
	info, _ := tt.AccessByRole(ui.RoleTextField, "唤醒模型目录")
	if !info.Disabled {
		t.Fatal("install destination can change mid-download")
	}
	nativeClick(t, tt, "连接")
	nativeVoiceEdit(t, tt, "设备地址", "new-draft.local")
	close(release)
	nativeFinishWake(t, a, updates, tt)
	if !a.wakeAssets.ready || len(f.saves) != 0 || !a.dirty || a.draft.Voice.LLMModel != "unfinished-model" || a.draft.Address != "new-draft.local" {
		t.Fatal("installation saved or replaced draft")
	}
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "离线唤醒")
	if !tt.HasText("已安装 · 你好小爱") || !tt.HasText("保存设置后使用此模型目录。") {
		t.Fatal("missing install result or unapplied path guidance")
	}
}

func TestNativeWakeCancelFailureAndRetry(t *testing.T) {
	a, _, updates, tt := nativeTest(t)
	tt.SetSize(1100, 2600)
	entered := make(chan struct{}, 1)
	a.wakeAssetOps.install = func(ctx context.Context, _ string, _ func(wake.InstallProgress)) error {
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "离线唤醒")
	nativeClick(t, tt, "安装唤醒模型")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("installer did not start")
	}
	nativeClick(t, tt, "取消安装")
	nativeFinishWake(t, a, updates, tt)
	if !tt.HasText("安装已取消，可重新安装") || a.wakeAssets.ready {
		t.Fatal("cancellation reported as success")
	}
	a.wakeAssetOps.install = func(context.Context, string, func(wake.InstallProgress)) error { return errors.New("HTTP 503") }
	nativeClick(t, tt, "重试安装")
	nativeFinishWake(t, a, updates, tt)
	if !tt.HasText("HTTP 503") || a.wakeAssets.ready {
		t.Fatal("failed download hidden")
	}
	a.wakeAssetOps.install = func(context.Context, string, func(wake.InstallProgress)) error { return nil }
	nativeClick(t, tt, "重试安装")
	nativeFinishWake(t, a, updates, tt)
	if !a.wakeAssets.ready {
		t.Fatal("retry failed")
	}
}
