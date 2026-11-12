package main

import (
	"testing"

	"aemeath/agent/internal/proto"
	"github.com/egoist/mygo/ui"
)

func TestNativeHeadersOmitComputerSubtitle(t *testing.T) {
	for _, page := range []string{"概览", "订阅", "设置", "语音助手"} {
		t.Run(page, func(t *testing.T) {
			a, _, _, _ := nativeTest(t)
			a.page = page
			for _, metrics := range []*proto.Frame{nil, {Host: "test-computer"}} {
				a.snapshot.Metrics = metrics
				tt := ui.NewTester(a.header, 900, 100)
				if _, ok := tt.Find(page); !ok {
					t.Fatal("page title missing")
				}
				for _, subtitle := range []string{"test-computer", "本机实时监控"} {
					if _, ok := tt.Find(subtitle); ok {
						t.Fatalf("header still contains subtitle %q", subtitle)
					}
				}
			}
		})
	}
}
