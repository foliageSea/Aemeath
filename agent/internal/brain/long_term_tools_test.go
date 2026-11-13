package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func memoryToolsFixture(store *LongTermMemory, user string) (*Engine, context.Context, *memoryTurnScope) {
	e := &Engine{longTerm: store, memoryMode: "explicit", generation: 7}
	scope := &memoryTurnScope{user: user, revision: store.Snapshot().Revision, seen: make(map[string]bool)}
	ctx := context.WithValue(context.Background(), requestScopeKey{}, requestScope{generation: e.generation})
	return e, context.WithValue(ctx, memoryTurnKey{}, scope), scope
}

func memoryToolForTest(t *testing.T, e *Engine, name string) *deviceTool {
	t.Helper()
	for _, candidate := range e.longTermTools() {
		info, err := candidate.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name == name {
			return candidate.(*deviceTool)
		}
	}
	t.Fatalf("missing tool %s", name)
	return nil
}

func invokeMemoryTool(t *testing.T, e *Engine, ctx context.Context, name string, args any, wantOK bool) string {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := memoryToolForTest(t, e, name).InvokableRun(ctx, string(data))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var report struct {
		OK      *bool  `json:"ok"`
		Message string `json:"message"`
	}
	if len(raw) > maxTextBytes || json.Unmarshal([]byte(raw), &report) != nil || report.OK == nil || *report.OK != wantOK || report.Message == "" {
		t.Fatalf("%s invalid result: %s", name, raw)
	}
	if name == "memory_search" {
		var search memorySearchToolReport
		if json.Unmarshal([]byte(raw), &search) != nil || search.Entries == nil {
			t.Fatalf("search must always return an entries array: %s", raw)
		}
	}
	return raw
}

func memorySaveArgs() map[string]string {
	return map[string]string{"key": "tea", "category": "preference", "content": "用户喜欢绿茶", "evidence": "我喜欢绿茶。"}
}

func TestLongTermToolsExplicitRoundTripIsDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long-term.json")
	store := OpenLongTermMemory(path)
	e, ctx, scope := memoryToolsFixture(store, "请记住：我喜欢绿茶。")
	ctx, cancel := context.WithCancel(ctx)
	raw := invokeMemoryTool(t, e, ctx, "memory_save", memorySaveArgs(), true)
	var saved memoryMutationToolReport
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Entry == nil || saved.Entry.ID == "" || saved.Entry.Source != "explicit" || saved.Entry.Evidence != "我喜欢绿茶。" || saved.Entry.CreatedAt.IsZero() || saved.Entry.UpdatedAt.IsZero() {
		t.Fatalf("bad explicit entry: %+v", saved.Entry)
	}
	if scope.revision != store.Snapshot().Revision || !scope.seen[saved.Entry.ID] || !scope.mutated {
		t.Fatalf("scope did not observe save: %+v", scope)
	}
	cancel() // A later reply cancellation does not roll back a committed tool.
	reopened := OpenLongTermMemory(path).Snapshot()
	if reopened.Error != "" || len(reopened.Entries) != 1 || reopened.Entries[0] != *saved.Entry {
		t.Fatalf("save not durable: %+v", reopened)
	}

	e, ctx, scope = memoryToolsFixture(store, "请忘记我对绿茶的偏好。")
	invokeMemoryTool(t, e, ctx, "memory_forget", map[string]string{"id": saved.Entry.ID}, false)
	invokeMemoryTool(t, e, ctx, "memory_search", map[string]string{"query": "绿茶"}, true)
	if !scope.seen[saved.Entry.ID] || scope.mutated {
		t.Fatal("search did not authorize returned ID or was treated as a mutation")
	}
	invokeMemoryTool(t, e, ctx, "memory_forget", map[string]string{"id": saved.Entry.ID}, true)
	if scope.seen[saved.Entry.ID] || scope.revision != store.Snapshot().Revision || !scope.mutated {
		t.Fatalf("delete left stale scope: %+v", scope)
	}
	if snapshot := OpenLongTermMemory(path).Snapshot(); snapshot.Error != "" || len(snapshot.Entries) != 0 {
		t.Fatalf("delete not durable: %+v", snapshot)
	}
}

func TestLongTermToolsSaveRequiresCurrentUserEvidence(t *testing.T) {
	cases := []struct {
		name, user string
		change     func(map[string]string)
	}{
		{"missing evidence", "请记住：我喜欢绿茶。", func(a map[string]string) { delete(a, "evidence") }},
		{"blank evidence", "请记住：我喜欢绿茶。", func(a map[string]string) { a["evidence"] = " " }},
		{"history is not evidence", "你好", func(a map[string]string) {}},
		{"non-exact evidence", "请记住：我喜欢绿茶。", func(a map[string]string) { a["evidence"] = "我偏爱绿茶。" }},
		{"evidence rune limit", strings.Repeat("茶", 513), func(a map[string]string) { a["evidence"] = strings.Repeat("茶", 513) }},
		{"invalid category", "请记住：我喜欢绿茶。", func(a map[string]string) { a["category"] = "secret" }},
		{"store validates key", "请记住：我喜欢绿茶。", func(a map[string]string) { a["key"] = "" }},
		{"store validates content", "请记住：我喜欢绿茶。", func(a map[string]string) { a["content"] = "" }},
		{"no explicit save request", "我喜欢绿茶。", func(a map[string]string) {}},
		{"explicit opt out", "不要记住我喜欢绿茶。", func(a map[string]string) {}},
		{"secret key", "请记住：我喜欢绿茶。", func(a map[string]string) { a["key"] = "sk-abcdefghijklmnopqrstuvwxyz0123456789" }},
		{"secret content", "请记住：我喜欢绿茶。", func(a map[string]string) { a["content"] = "api_key=sk-abcdefghijklmnopqrstuvwxyz0123456789" }},
		{"secret evidence", "请记住api_key=sk-abcdefghijklmnopqrstuvwxyz0123456789", func(a map[string]string) { a["evidence"] = "api_key=sk-abcdefghijklmnopqrstuvwxyz0123456789" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := OpenLongTermMemory("")
			e, ctx, scope := memoryToolsFixture(store, tc.user)
			args := memorySaveArgs()
			tc.change(args)
			raw := invokeMemoryTool(t, e, ctx, "memory_save", args, false)
			if strings.Contains(raw, args["content"]) && args["content"] != "" {
				t.Fatal("failure echoed private content")
			}
			if len(store.Snapshot().Entries) != 0 || scope.revision != 0 || len(scope.seen) != 0 || scope.mutated {
				t.Fatal("rejected save changed memory or scope")
			}
		})
	}
	// Rune count, rather than byte count, permits 512 Chinese evidence characters.
	store := OpenLongTermMemory("")
	evidence := strings.Repeat("茶", 512)
	e, ctx, _ := memoryToolsFixture(store, "请记住："+evidence)
	args := memorySaveArgs()
	args["evidence"] = evidence
	invokeMemoryTool(t, e, ctx, "memory_save", args, true)
}

func TestLongTermToolsRecallDoesNotAuthorizeDeletion(t *testing.T) {
	for _, user := range []string{"帮我查天气", "不要忘记我对绿茶的偏好", "Do not forget my tea preference"} {
		t.Run(user, func(t *testing.T) {
			store := OpenLongTermMemory("")
			entry, err := store.Save(MemoryEntry{Key: "tea", Category: "preference", Content: "用户喜欢绿茶"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			e, ctx, scope := memoryToolsFixture(store, user)
			scope.seen[entry.ID] = true
			invokeMemoryTool(t, e, ctx, "memory_forget", map[string]string{"id": entry.ID}, false)
			if len(store.Snapshot().Entries) != 1 || scope.mutated {
				t.Fatal("recall alone authorized deletion")
			}
		})
	}
}

func TestLongTermToolsRequireLiveRequestAndTurn(t *testing.T) {
	for _, name := range []string{"memory_search", "memory_save", "memory_forget"} {
		for _, condition := range []string{"no request", "no turn", "nil turn", "closed", "generation", "chat epoch", "chat revision", "summary", "off", "no store"} {
			t.Run(name+"/"+condition, func(t *testing.T) {
				store := OpenLongTermMemory("")
				entry, err := store.Save(MemoryEntry{Key: "tea", Category: "preference", Content: "用户喜欢红茶", Source: "manual"}, 0)
				if err != nil {
					t.Fatal(err)
				}
				e, ctx, scope := memoryToolsFixture(store, "请记住：我喜欢绿茶。")
				scope.seen[entry.ID] = true
				var args any = memorySaveArgs()
				switch name {
				case "memory_search":
					args = map[string]string{"query": "茶"}
				case "memory_forget":
					args = map[string]string{"id": entry.ID}
				}
				switch condition {
				case "no request":
					ctx = context.WithValue(context.Background(), memoryTurnKey{}, scope)
				case "no turn":
					ctx = context.WithValue(context.Background(), requestScopeKey{}, requestScope{generation: e.generation})
				case "nil turn":
					ctx = context.WithValue(ctx, memoryTurnKey{}, (*memoryTurnScope)(nil))
				case "closed":
					e.closed = true
				case "generation":
					e.generation++
				case "chat epoch":
					e.memory = &Memory{epoch: 1}
				case "chat revision":
					e.memory = &Memory{revision: 1}
				case "summary":
					ctx = context.WithValue(ctx, requestScopeKey{}, requestScope{generation: e.generation, summary: true})
				case "off":
					e.memoryMode = "off"
				case "no store":
					e.longTerm = nil
				}
				invokeMemoryTool(t, e, ctx, name, args, false)
				if snapshot := store.Snapshot(); snapshot.Revision != 1 || len(snapshot.Entries) != 1 || snapshot.Entries[0] != entry {
					t.Fatal("invalid scope mutated memory")
				}
			})
		}
	}
}

func TestLongTermToolsVersionConflictNeedsNewSearch(t *testing.T) {
	store := OpenLongTermMemory("")
	e, ctx, scope := memoryToolsFixture(store, "请记住：我喜欢绿茶。")
	external, err := store.Save(MemoryEntry{Key: "tea", Category: "preference", Content: "用户喜欢红茶", Source: "manual"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw := invokeMemoryTool(t, e, ctx, "memory_save", memorySaveArgs(), false)
	if !strings.Contains(raw, "版本已变化") || scope.revision != 0 {
		t.Fatalf("missed save conflict: %s", raw)
	}
	invokeMemoryTool(t, e, ctx, "memory_search", map[string]string{"query": "茶"}, true)
	if scope.revision != 1 || !scope.seen[external.ID] {
		t.Fatalf("search did not refresh scope: %+v", scope)
	}
	_, err = store.Save(MemoryEntry{Key: "other", Category: "fact", Content: "外部修改", Source: "manual"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw = invokeMemoryTool(t, e, ctx, "memory_forget", map[string]string{"id": external.ID}, false)
	if !strings.Contains(raw, "版本已变化") || len(store.Snapshot().Entries) != 2 || scope.revision != 1 {
		t.Fatalf("missed delete conflict: %s", raw)
	}
	invokeMemoryTool(t, e, ctx, "memory_search", map[string]string{"query": "茶"}, true)
	invokeMemoryTool(t, e, ctx, "memory_save", memorySaveArgs(), true)
	if snapshot := store.Snapshot(); snapshot.Revision != 3 || scope.revision != 3 || len(snapshot.Entries) != 2 {
		t.Fatalf("retry failed: %+v", snapshot)
	}
}

func TestLongTermToolsSearchBoundsAndResetsSeen(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprintf("large=%v", large), func(t *testing.T) {
			store := OpenLongTermMemory("")
			for i := 0; i < 12; i++ {
				entry := MemoryEntry{Key: fmt.Sprintf("tea-%d", i), Category: "fact", Content: "tea", Source: "manual"}
				if large {
					entry.Evidence = strings.Repeat("<", 512)
				}
				if _, err := store.Save(entry, store.Snapshot().Revision); err != nil {
					t.Fatal(err)
				}
			}
			e, ctx, scope := memoryToolsFixture(store, "查询tea")
			scope.seen["unrelated"] = true
			scope.revision = 0
			raw := invokeMemoryTool(t, e, ctx, "memory_search", map[string]string{"query": "tea"}, true)
			var report memorySearchToolReport
			if err := json.Unmarshal([]byte(raw), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Entries) == 0 || len(report.Entries) > 10 || (!large && len(report.Entries) != 10) || (large && len(report.Entries) >= 10) {
				t.Fatalf("unexpected bounded result count: %d", len(report.Entries))
			}
			if scope.revision != 12 || scope.seen["unrelated"] || len(scope.seen) != len(report.Entries) {
				t.Fatalf("search scope mismatch: %+v", scope)
			}
			for _, entry := range report.Entries {
				if !scope.seen[entry.ID] {
					t.Fatal("returned ID not authorized")
				}
			}
			invokeMemoryTool(t, e, ctx, "memory_search", map[string]string{"query": "no-match-xyz"}, true)
			if len(scope.seen) != 0 {
				t.Fatal("empty search retained previous delete permissions")
			}
		})
	}
	store := OpenLongTermMemory("")
	e, ctx, _ := memoryToolsFixture(store, "查询记忆")
	for _, query := range []string{"", "  ", strings.Repeat("茶", 257)} {
		invokeMemoryTool(t, e, ctx, "memory_search", map[string]string{"query": query}, false)
	}
	invokeMemoryTool(t, e, ctx, "memory_search", map[string]string{"query": strings.Repeat("茶", 256)}, true)
	for _, name := range []string{"memory_search", "memory_save", "memory_forget"} {
		for _, args := range []string{"{", `{"unknown":"private"}`, string([]byte{255})} {
			raw, err := memoryToolForTest(t, e, name).InvokableRun(ctx, args)
			if err != nil || !strings.Contains(raw, `"ok":false`) || strings.Contains(raw, "private") {
				t.Fatalf("unsafe parameter failure: %s %v", raw, err)
			}
		}
	}
}

func TestLongTermToolsCanceledRequestsDoNotMutate(t *testing.T) {
	for _, name := range []string{"memory_search", "memory_save", "memory_forget"} {
		t.Run(name, func(t *testing.T) {
			store := OpenLongTermMemory("")
			e, ctx, scope := memoryToolsFixture(store, "请记住：我喜欢绿茶。")
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			raw, err := memoryToolForTest(t, e, name).InvokableRun(ctx, `{}`)
			if !errors.Is(err, context.Canceled) || raw != "" || scope.revision != 0 || len(store.Snapshot().Entries) != 0 {
				t.Fatalf("canceled operation: %s %v", raw, err)
			}
		})
	}
	// A context canceled while waiting for Engine.mu must be checked again.
	store := OpenLongTermMemory("")
	e, ctx, _ := memoryToolsFixture(store, "请记住：我喜欢绿茶。")
	ctx, cancel := context.WithCancel(ctx)
	selected := memoryToolForTest(t, e, "memory_save")
	e.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := selected.run(ctx, `{"key":"tea","category":"preference","content":"用户喜欢绿茶","evidence":"我喜欢绿茶。"}`)
		done <- err
	}()
	cancel()
	e.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation after lock wait: %v", err)
	}
	if len(store.Snapshot().Entries) != 0 {
		t.Fatal("canceled waiter committed memory")
	}
}

func TestLongTermToolsPersistenceFailureKeepsSnapshotAndAllowsRetry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "private-directory")
	path := filepath.Join(dir, "long-term.json")
	store := OpenLongTermMemory(path)
	e, ctx, scope := memoryToolsFixture(store, "请记住：我喜欢绿茶。")
	invokeMemoryTool(t, e, ctx, "memory_save", memorySaveArgs(), true)
	baseline := store.Snapshot()
	id := baseline.Entries[0].ID
	moved := filepath.Join(root, "moved-directory")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("block directory recreation"), 0600); err != nil {
		t.Fatal(err)
	}
	args := memorySaveArgs()
	args["key"] = "second"
	for _, name := range []string{"memory_save", "memory_forget"} {
		var input any = args
		if name == "memory_forget" {
			input = map[string]string{"id": id}
		}
		raw := invokeMemoryTool(t, e, ctx, name, input, false)
		if strings.Contains(raw, root) || strings.Contains(raw, "private-directory") || strings.Contains(raw, "用户喜欢绿茶") {
			t.Fatalf("failure leaked details: %s", raw)
		}
		current := store.Snapshot()
		if current.Error == "" || current.Status == "unavailable" || current.Revision != baseline.Revision || len(current.Entries) != 1 || current.Entries[0] != baseline.Entries[0] || scope.revision != baseline.Revision || !scope.seen[id] {
			t.Fatalf("failed commit published mutation: %+v", current)
		}
	}
	invokeMemoryTool(t, e, ctx, "memory_search", map[string]string{"query": "绿茶"}, true)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, dir); err != nil {
		t.Fatal(err)
	}
	invokeMemoryTool(t, e, ctx, "memory_save", args, true)
	if snapshot := store.Snapshot(); snapshot.Error != "" || len(snapshot.Entries) != 2 || snapshot.Revision != baseline.Revision+1 {
		t.Fatalf("retry did not recover: %+v", snapshot)
	}
}

func TestLongTermToolsUnavailableStoreFailsSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-memory.json")
	if err := os.WriteFile(path, []byte("private-invalid-payload"), 0600); err != nil {
		t.Fatal(err)
	}
	store := OpenLongTermMemory(path)
	e, ctx, _ := memoryToolsFixture(store, "请记住：我喜欢绿茶。")
	for _, name := range []string{"memory_search", "memory_save", "memory_forget"} {
		raw := invokeMemoryTool(t, e, ctx, name, memorySaveArgs(), false)
		if !strings.Contains(raw, "暂不可用") || strings.Contains(raw, path) || strings.Contains(raw, "private-invalid-payload") {
			t.Fatalf("unsafe unavailable failure: %s", raw)
		}
	}
}

func TestLongTermToolsObserverNeverDisplaysMemoryPayloads(t *testing.T) {
	for _, name := range []string{"memory_search", "memory_save", "memory_forget"} {
		for _, result := range []string{
			`{"ok":true,"entries":[{"content":"private sk-secret"}],"message":"private sk-secret"}`,
			`{"ok":false,"message":"private sk-secret"}`,
			`{"message":"private sk-secret"}`,
			"private invalid sk-secret",
		} {
			t.Run(name+"/"+result[:8], func(t *testing.T) {
				var events []ToolCall
				ctx := WithToolObserver(context.Background(), func(event ToolCall) { events = append(events, event) })
				finish := startTool(ctx, name, `{"content":"private sk-secret","evidence":"private sk-secret"}`)
				finish(result, nil)
				if len(events) != 2 || events[0].Arguments != "{}" || events[1].Arguments != "{}" {
					t.Fatalf("unsafe memory arguments: %+v", events)
				}
				for _, event := range events {
					data, _ := json.Marshal(event)
					if strings.Contains(string(data), "private") || strings.Contains(string(data), "sk-secret") {
						t.Fatalf("payload leaked: %s", data)
					}
				}
				wantStatus := "failed"
				if strings.Contains(result, `"ok":true`) {
					wantStatus = "succeeded"
				}
				if events[1].Status != wantStatus || !strings.HasPrefix(events[1].Result, "长期记忆") {
					t.Fatalf("bad fixed outcome: %+v", events[1])
				}
			})
		}
	}
}
