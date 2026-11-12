package main

import "testing"

func TestNativeReasoningEffortSavedAndReopened(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 2600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "助手")
	nativeClick(t, tt, "对话服务")
	for _, tc := range []struct{ label, value string }{{"低", "low"}, {"中", "medium"}, {"高", "high"}, {"默认", ""}, {"低", "low"}} {
		nativeClick(t, tt, tc.label)
		if a.draft.Voice.ReasoningEffort != tc.value || !a.dirty {
			t.Fatalf("selection %s: effort=%q dirty=%v", tc.label, a.draft.Voice.ReasoningEffort, a.dirty)
		}
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	saved := f.saves[0].Voice.ReasoningEffort
	f.mu.Unlock()
	if saved != "low" || nativeState(f).draft.Voice.ReasoningEffort != "low" {
		t.Fatal("reasoning effort lost during save or reopen")
	}
}
