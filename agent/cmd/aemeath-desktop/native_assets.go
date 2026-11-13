package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aemeath/agent/internal/wake"
	"github.com/egoist/mygo/ui"
)

type nativeWakeAssetOps struct {
	verify  func(string) error
	install func(context.Context, string, func(wake.InstallProgress)) error
}

type nativeWakeAssets struct {
	initialCheck bool
	path         string
	checked      bool
	ready        bool
	installing   bool
	canceled     bool
	err          error
	progress     wake.InstallProgress
	cancel       context.CancelFunc
	generation   uint64
}

type nativeWakeReloader interface{ ReloadWakeModels(string) error }

// Resource installation uses the saved service policy, never the settings draft.
type nativeNetworkService interface {
	NetworkContext(context.Context) context.Context
}

func (a *nativeUI) wakeModelPath() string {
	path := strings.TrimSpace(a.draft.Voice.WakeModelDir)
	if path == "" {
		return wake.DefaultModelDir()
	}
	return filepath.Clean(path)
}

// All state changes, including download progress, are dispatched to the UI thread.
func (a *nativeUI) startWakeAssets(install bool) {
	if a.busy["wake-assets"] {
		return
	}
	dispatch := a.update
	if dispatch == nil {
		a.notify(nativeNoticeWarning, "窗口尚未就绪，请稍后重试。")
		return
	}
	target := a.wakeModelPath()
	verify, installer := a.wakeAssetOps.verify, a.wakeAssetOps.install
	if verify == nil {
		verify = wake.VerifyWakeModels
	}
	if installer == nil {
		installer = wake.InstallWakeModels
	}
	parent := context.Background()
	if service, ok := a.service.(nativeNetworkService); ok {
		parent = service.NetworkContext(parent)
	}
	ctx, cancel := context.WithCancel(parent)
	generation := a.wakeAssets.generation + 1
	a.wakeAssets = nativeWakeAssets{initialCheck: true, path: target, installing: install, cancel: cancel, generation: generation, progress: wake.InstallProgress{Stage: "checking"}}
	a.busy["wake-assets"] = true
	go func() {
		defer cancel()
		var err error
		if install {
			err = installer(ctx, target, func(progress wake.InstallProgress) {
				dispatch(func() {
					if a.wakeAssets.generation == generation && a.busy["wake-assets"] {
						a.wakeAssets.progress = progress
					}
				})
			})
		} else {
			err = verify(target)
		}
		dispatch(func() {
			if a.wakeAssets.generation != generation {
				return
			}
			a.busy["wake-assets"] = false
			a.wakeAssets.cancel = nil
			a.wakeAssets.checked, a.wakeAssets.ready = true, err == nil
			a.wakeAssets.canceled = errors.Is(err, context.Canceled)
			a.wakeAssets.err = err
			if !install {
				return
			}
			if err != nil {
				if !a.wakeAssets.canceled {
					a.notify(nativeNoticeError, "唤醒模型安装失败，请检查网络或目录后重试。")
				}
				return
			}
			a.notify(nativeNoticeSuccess, "唤醒模型已安装。")
			// Reload only the saved model directory; never save or overwrite a draft.
			saved := strings.TrimSpace(a.snapshot.Settings.Voice.WakeModelDir)
			if saved == "" {
				saved = wake.DefaultModelDir()
			}
			if service, ok := a.service.(nativeWakeReloader); ok && filepath.Clean(saved) == target && a.snapshot.Settings.Voice.Enabled && a.snapshot.Running {
				a.run("wake-reload", func() error { return service.ReloadWakeModels(target) }, func() { a.notify(nativeNoticeSuccess, "唤醒模型已安装，语音连接将重新加载模型。") })
			}
		})
	}()
}

func (a *nativeUI) cancelWakeAssets() {
	if a.wakeAssets.cancel != nil {
		a.wakeAssets.cancel()
	}
}

func nativeWakeStage(stage string) string {
	switch stage {
	case "downloading-model":
		return "正在下载唤醒模型"
	case "extracting":
		return "正在解压唤醒模型"
	case "downloading-vad":
		return "正在下载语音检测模型"
	case "verifying":
		return "正在校验模型"
	case "complete":
		return "安装完成"
	default:
		return "正在检查本地模型"
	}
}

func (a *nativeUI) wakeModelsView(c *ui.Context) {
	t := c.Theme()
	target := a.wakeModelPath()
	if !a.wakeAssets.initialCheck && a.update != nil {
		a.startWakeAssets(false)
	}
	state, busy := a.wakeAssets, a.busy["wake-assets"]
	current := state.path == target
	title, color := "尚未检查", t.TextMuted
	if busy {
		title = nativeWakeStage(state.progress.Stage)
	} else if current && state.checked {
		switch {
		case state.ready:
			title, color = "已安装 · 中文离线唤醒模型", t.Success
		case state.canceled:
			title = "安装已取消，可重新安装"
		case errors.Is(state.err, os.ErrNotExist):
			title = "未安装"
		default:
			title, color = "模型校验或安装失败", t.Warning
		}
	}
	nativeSettingsSection(c, "wake-model-card", "", func() {
		ui.Text(c, "离线唤醒模型").FontSize(15).Bold()
		ui.Text(c, title).FontSize(12).TextColor(color).Wrap()
		a.wakeDirectoryView(c, busy)
		nativeSettingsActions(c, "wake-model-actions", func() {
			if busy && state.installing {
				ui.Button(c.Key("wake-install-cancel"), "取消安装").OnClick(a.cancelWakeAssets)
			} else {
				label := "安装唤醒模型"
				if current && state.checked && !state.ready && !errors.Is(state.err, os.ErrNotExist) {
					label = "重试安装"
				}
				ui.PrimaryButton(c.Key("wake-install"), label).Disabled(busy || current && state.ready).OnClick(func() { a.startWakeAssets(true) })
				ui.Button(c.Key("wake-check"), "检查模型").Disabled(busy).OnClick(func() { a.startWakeAssets(false) })
			}
		})
		if busy && state.installing {
			if state.progress.Total > 0 {
				p := state.progress
				ui.Progress(c, float64(p.Downloaded)/float64(p.Total)).FillWidth()
				ui.Text(c, fmt.Sprintf("%.1f / %.1f MB", float64(p.Downloaded)/(1<<20), float64(p.Total)/(1<<20))).FontSize(11).TextColor(t.TextMuted)
			} else {
				ui.Text(c, "请稍候…").FontSize(11).TextColor(t.TextMuted)
			}
		} else if current && state.err != nil && !state.canceled && !errors.Is(state.err, os.ErrNotExist) {
			ui.Text(c, state.err.Error()).FontSize(11).TextColor(t.Warning).Wrap().MaxLines(3).Ellipsis("…").Tooltip(state.err.Error()).Selectable()
		}
		ui.Text(c, "首次下载约 33 MB，包含唤醒与语音检测模型。安装完成后可离线使用。").FontSize(11).TextColor(t.TextMuted).Wrap()
		if current && state.ready && filepath.Clean(a.snapshot.Settings.Voice.WakeModelDir) != target {
			ui.Text(c, "保存设置后使用此模型目录。").FontSize(11).TextColor(t.TextMuted)
		}
	})
}
