package main

import "github.com/egoist/mygo/ui"

type nativeNoticeType string

const (
	nativeNoticeSuccess nativeNoticeType = "success"
	nativeNoticeError   nativeNoticeType = "error"
	nativeNoticeWarning nativeNoticeType = "warning"
	nativeNoticeInfo    nativeNoticeType = "info"
)

var (
	toastSuccessIcon = sidebarSVG("<circle cx='12' cy='12' r='9'/><path d='m8 12 3 3 5-6'/>")
	toastErrorIcon   = sidebarSVG("<circle cx='12' cy='12' r='9'/><path d='m9 9 6 6m0-6-6 6'/>")
	toastWarningIcon = sidebarSVG("<path d='M10.3 4a2 2 0 0 1 3.4 0l8 14a2 2 0 0 1-1.7 3H4a2 2 0 0 1-1.7-3Z'/><path d='M12 9v4m0 4h.01'/>")
	toastInfoIcon    = sidebarSVG("<circle cx='12' cy='12' r='9'/><path d='M12 11v6m0-10h.01'/>")
)

func (a *nativeUI) toastView(c *ui.Context) {
	t := c.Theme()
	width, _ := c.Size()
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Top(max(float32(nativeTitleBarHeight), c.TitleBar().Height)+12).
			Padding(0, 16, 16).Justify(ui.Start).AlignItems(ui.Center).Gap(8)
		for _, toast := range toasts {
			label, color, icon := "信息", t.Accent, toastInfoIcon
			switch nativeNoticeType(toast.Type) {
			case nativeNoticeSuccess:
				label, color, icon = "成功", t.Success, toastSuccessIcon
			case nativeNoticeError:
				label, color, icon = "错误", t.Danger, toastErrorIcon
			case nativeNoticeWarning:
				label, color, icon = "警告", t.Warning, toastWarningIcon
			}
			parts := ui.ToastBase(c, toast)
			parts.Root.Label(label+"提醒").Row().AlignItems(ui.Center).Gap(10).
				MaxWidth(max(float32(0), width-32)).Padding(12, 16).Radius(10).
				Background(t.Surface).BorderWidth(1).BorderColor(color.Alpha(0.45)).
				Shadow(0, 4, 16, 0, ui.RGBA(0, 0, 0, 0.15)).Children(func() {
				ui.Icon(c, icon).Label(label + "图标").FontSize(18).TextColor(color).Shrink(0)
				ui.Text(c, toast.Title).FontSize(13).TextColor(t.Text).Shrink(1).MinWidth(0).Wrap()
				parts.CloseButton().Label("关闭提醒").Size(22, 22).Radius(4).Children(func() {
					ui.Text(c, "×").TextColor(t.TextMuted)
				})
			})
		}
	})
}
