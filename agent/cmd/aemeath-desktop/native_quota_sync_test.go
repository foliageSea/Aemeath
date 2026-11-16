package main

import (
	"reflect"
	"testing"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/proto"
	"github.com/egoist/mygo/ui"
)

func TestNativeQuotaMembershipFollowsSuccessfulSnapshot(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	nativeClick(t, tt, "订阅")
	f.state.Quota = &proto.QuotaFrame{Accounts: []proto.QuotaAccount{{Name: "C"}, {Name: "A"}, {Name: "New"}}}
	a.refresh()
	tt.Frame()
	assertQuotaNames(t, a, "A", "C", "New")
	if _, exists := tt.AccessByRole(ui.RoleListItem, "B"); exists {
		t.Fatal("removed account still has a card")
	}
	if len(a.draft.Subscriptions) != 3 {
		t.Fatal("refresh discarded historical preferences")
	}
	// A failed request retains the last successful frame in the service.
	f.state.QuotaError = "gateway unavailable"
	a.refresh()
	tt.Frame()
	assertQuotaNames(t, a, "A", "C", "New")
	// Successful empty snapshots remove every row, unlike request failures.
	f.state.QuotaError = ""
	f.state.Quota = &proto.QuotaFrame{Accounts: []proto.QuotaAccount{}}
	a.refresh()
	tt.Frame()
	assertQuotaNames(t, a)
	if _, exists := tt.AccessByRole(ui.RoleListItem, "A"); exists {
		t.Fatal("empty snapshot retained a card")
	}
	a.snapshot.Quota = nil
	assertQuotaNames(t, a)
}

func TestNativeQuotaAbsentPreferencesSurviveLiveEditsAndRestart(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	a.draft.Subscriptions = []desktop.Subscription{{Name: "A"}, {Name: "Absent", Hidden: true}, {Name: "B"}, {Name: "C"}}
	nativeClick(t, tt, "订阅")
	quotaPointerDrag(t, tt, "C", "A", true)
	nativeComplete(t, updates, tt)
	assertQuotaNames(t, a, "C", "A", "B")
	nativeClick(t, tt, "隐藏 B")
	nativeComplete(t, updates, tt)
	want := []desktop.Subscription{{Name: "C"}, {Name: "Absent", Hidden: true}, {Name: "A"}, {Name: "B", Hidden: true}}
	if !reflect.DeepEqual(f.state.Settings.Subscriptions, want) {
		t.Fatalf("saved preferences = %+v, want %+v", f.state.Settings.Subscriptions, want)
	}
	// Simulate restart by reloading the saved settings, then re-add the account.
	restarted := &nativeUI{draft: copySettings(f.state.Settings), snapshot: f.Snapshot()}
	assertQuotaNames(t, restarted, "C", "A", "B")
	restarted.snapshot.Quota = &proto.QuotaFrame{Accounts: []proto.QuotaAccount{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "Absent"}, {Name: "New"}}}
	assertQuotaNames(t, restarted, "C", "Absent", "A", "B", "New")
	if !restarted.subscriptions()[1].Hidden {
		t.Fatal("re-added account lost hidden preference")
	}
}

func TestMergeSubscriptionPreferences(t *testing.T) {
	for _, tc := range []struct {
		name              string
		saved, live, want []desktop.Subscription
	}{
		{"new accounts", nil, []desktop.Subscription{{Name: "A"}}, []desktop.Subscription{{Name: "A"}}},
		{"all absent", []desktop.Subscription{{Name: "Old", Hidden: true}}, nil, []desktop.Subscription{{Name: "Old", Hidden: true}}},
		{"new reordered before saved", []desktop.Subscription{{Name: "A"}, {Name: "Old", Hidden: true}}, []desktop.Subscription{{Name: "New"}, {Name: "A", Hidden: true}}, []desktop.Subscription{{Name: "New"}, {Name: "Old", Hidden: true}, {Name: "A", Hidden: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := append([]desktop.Subscription(nil), tc.saved...)
			live := append([]desktop.Subscription(nil), tc.live...)
			if got := mergeSubscriptionPreferences(tc.saved, tc.live); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.saved, saved) || !reflect.DeepEqual(tc.live, live) {
				t.Fatal("merge mutated input")
			}
		})
	}
}
