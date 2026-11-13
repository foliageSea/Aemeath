package brain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func ltmEntry(key, content string) MemoryEntry {
	return MemoryEntry{Key: key, Category: "fact", Content: content}
}

func ltmSave(t *testing.T, m *LongTermMemory, entry MemoryEntry) MemoryEntry {
	t.Helper()
	saved, err := m.Save(entry, m.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestLongTermMemoryRestartAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "memory.json")
	m := OpenLongTermMemory(path)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load created file: %v", err)
	}
	entry := ltmEntry("Home City", "住在杭州")
	entry.Evidence = "原文  保留\n换行"
	saved := ltmSave(t, m, entry)
	if len(saved.ID) != 32 || saved.Source != "manual" || saved.CreatedAt.Location() != time.UTC || saved.UpdatedAt.Location() != time.UTC {
		t.Fatalf("unexpected identity/timestamps: %+v", saved)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions: %v %v", info, err)
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v %v", info, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := OpenLongTermMemory(path)
	snapshot := restarted.Snapshot()
	if snapshot.Error != "" || snapshot.Revision != 1 || snapshot.Epoch != 0 || !reflect.DeepEqual(snapshot.Entries, m.Snapshot().Entries) {
		t.Fatalf("restart changed memory: %+v", snapshot)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("load rewrote file: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(before, &wire); err != nil {
		t.Fatal(err)
	}
	item := wire["entries"].([]any)[0].(map[string]any)
	if item["createdAt"] == nil || item["updatedAt"] == nil || item["evidence"] != entry.Evidence {
		t.Fatalf("unexpected JSON: %s", before)
	}
	if err := restarted.Clear(snapshot.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("clear removed persistent tombstone: %v", err)
	}
	if got := OpenLongTermMemory(path).Snapshot(); len(got.Entries) != 0 || got.Revision != 2 || got.Error != "" {
		t.Fatalf("cleared data revived: %+v", got)
	}
}

func TestLongTermMemoryUnicodeValidation(t *testing.T) {
	m := OpenLongTermMemory("")
	entry := ltmEntry(strings.Repeat("键", 80), strings.Repeat("😀", 256))
	entry.Evidence = strings.Repeat("证", 512)
	saved := ltmSave(t, m, entry)
	if saved.Content != entry.Content || saved.Evidence != entry.Evidence {
		t.Fatal("Unicode content was truncated")
	}
	tests := []struct {
		name  string
		entry MemoryEntry
		want  error
	}{
		{"key", ltmEntry(strings.Repeat("键", 81), "ok"), ErrMemoryLimit},
		{"content", ltmEntry("k", strings.Repeat("😀", 257)), ErrMemoryLimit},
		{"evidence", MemoryEntry{Key: "k", Category: "fact", Content: "ok", Evidence: strings.Repeat("证", 513)}, ErrMemoryLimit},
		{"empty key", ltmEntry(" \t", "ok"), ErrMemoryInvalid},
		{"empty content", ltmEntry("k", " \n"), ErrMemoryInvalid},
		{"category", MemoryEntry{Key: "k", Category: "other", Content: "ok"}, ErrMemoryInvalid},
		{"source", MemoryEntry{Key: "k", Category: "fact", Content: "ok", Source: "other"}, ErrMemoryInvalid},
		{"utf8", ltmEntry("k", string([]byte{0xff})), ErrMemoryInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := m.Snapshot()
			if _, err := m.Save(tt.entry, before.Revision); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(before, m.Snapshot()) {
				t.Fatal("validation changed store")
			}
		})
	}
	for input, want := range map[string]string{"": "auto", " AUTO ": "auto", "explicit": "explicit", "off": "off"} {
		if got, err := NormalizeMemoryMode(input); err != nil || got != want {
			t.Fatalf("mode %q: %q %v", input, got, err)
		}
	}
	if _, err := NormalizeMemoryMode("on"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestLongTermMemoryUpsertCorrectionAndCopies(t *testing.T) {
	m := OpenLongTermMemory("")
	first := ltmSave(t, m, ltmEntry("  HOME\t City  ", "  住在  上海\n"))
	corrected := ltmEntry("home   CITY", "住在杭州")
	corrected.Source = "explicit"
	second := ltmSave(t, m, corrected)
	if first.Key != "home city" || first.Content != "住在 上海" || first.ID != second.ID || !first.CreatedAt.Equal(second.CreatedAt) || second.Source != "explicit" || len(m.Snapshot().Entries) != 1 {
		t.Fatalf("key upsert did not correct: %+v %+v", first, second)
	}
	second.Key, second.Content = "current city", "住在北京"
	third := ltmSave(t, m, second)
	if third.ID != first.ID || third.Key != "current city" || len(m.Snapshot().Entries) != 1 {
		t.Fatal("ID update created duplicate")
	}
	other := ltmSave(t, m, ltmEntry("other", "unchanged"))
	third.Key = other.Key
	if _, err := m.Save(third, m.Snapshot().Revision); !errors.Is(err, ErrMemoryInvalid) {
		t.Fatalf("key collision: %v", err)
	}
	snapshot := m.Snapshot()
	snapshot.Entries[0].Content = "mutated copy"
	found := m.Search("北京", 1)
	found[0].Content = "mutated search"
	if m.Snapshot().Entries[0].Content != "住在北京" {
		t.Fatal("returned slice aliases store")
	}
	if err := m.Delete(other.ID, m.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot().Entries) != 1 {
		t.Fatal("delete removed wrong data")
	}
	if err := m.Delete("absent", m.Snapshot().Revision); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("missing delete: %v", err)
	}
}

func TestLongTermMemoryRevisionAndEpoch(t *testing.T) {
	m := OpenLongTermMemory("")
	first := ltmSave(t, m, ltmEntry("one", "one"))
	m.SetStatus("extracting")
	before := m.Snapshot()
	if before.Revision != 1 || before.Epoch != 1 || before.Status != "extracting" {
		t.Fatalf("unexpected status/revision: %+v", before)
	}
	if _, err := m.Save(ltmEntry("two", "two"), 0); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("save conflict: %v", err)
	}
	if err := m.Delete(first.ID, 0); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("delete conflict: %v", err)
	}
	if err := m.Clear(0); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("clear conflict: %v", err)
	}
	if err := m.ApplyAuto([]MemoryEntry{ltmEntry("two", "two")}, 0); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("auto conflict: %v", err)
	}
	if !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("conflict changed state")
	}
	m.Invalidate()
	after := m.Snapshot()
	if after.Revision != before.Revision+1 || after.Epoch != before.Epoch+1 || after.Status != "ready" || !reflect.DeepEqual(after.Entries, before.Entries) {
		t.Fatal("invalidation changed entries")
	}
	if err := m.ApplyAuto([]MemoryEntry{ltmEntry("two", "two")}, before.Revision); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale task survived invalidation: %v", err)
	}
	if err := m.ApplyAuto([]MemoryEntry{ltmEntry("two", "two")}, after.Revision); err != nil {
		t.Fatal(err)
	}
	auto := m.Snapshot()
	if auto.Revision != after.Revision+1 || auto.Epoch != after.Epoch {
		t.Fatal("auto write invalidated pending epoch")
	}
	if err := m.Delete(first.ID, auto.Revision); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot(); got.Epoch != auto.Epoch+1 {
		t.Fatal("delete did not advance epoch")
	}
	beforeClear := m.Snapshot()
	if err := m.Clear(beforeClear.Revision); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot(); got.Epoch != beforeClear.Epoch+1 {
		t.Fatal("clear did not advance epoch")
	}
}

func TestLongTermMemoryManualMutationResetsWorkStatus(t *testing.T) {
	for _, status := range []string{"queued", "extracting"} {
		t.Run(status, func(t *testing.T) {
			m := OpenLongTermMemory("")
			m.SetStatus(status)
			saved := ltmSave(t, m, ltmEntry("key", "value"))
			if m.Snapshot().Status != "ready" {
				t.Fatal("save left stale work status")
			}
			m.SetStatus(status)
			if err := m.Delete(saved.ID, m.Snapshot().Revision); err != nil {
				t.Fatal(err)
			}
			if m.Snapshot().Status != "ready" {
				t.Fatal("delete left stale work status")
			}
			m.SetStatus(status)
			m.Invalidate()
			if m.Snapshot().Status != "ready" {
				t.Fatal("invalidation left stale work status")
			}
			m.SetStatus("paused")
			m.Invalidate()
			if m.Snapshot().Status != "paused" {
				t.Fatal("invalidation overwrote unrelated status")
			}
		})
	}
}

func TestLongTermMemoryAutoPriorityAndTransaction(t *testing.T) {
	m := OpenLongTermMemory("")
	manual := ltmSave(t, m, ltmEntry("city", "杭州"))
	explicitEntry := ltmEntry("name", "小明")
	explicitEntry.Source = "explicit"
	explicit := ltmSave(t, m, explicitEntry)
	if err := m.ApplyAuto([]MemoryEntry{ltmEntry("CITY", "上海"), {ID: explicit.ID, Key: "name", Category: "profile", Content: "小红"}, ltmEntry("drink", "茶")}, m.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	snapshot := m.Snapshot()
	if len(snapshot.Entries) != 3 || snapshot.Entries[0] != manual || snapshot.Entries[1] != explicit || snapshot.Entries[2].Source != "auto" {
		t.Fatalf("auto overwrote protected data: %+v", snapshot)
	}
	autoID := snapshot.Entries[2].ID
	if err := m.ApplyAuto([]MemoryEntry{ltmEntry("drink", "咖啡"), ltmEntry(" DRINK ", "水")}, snapshot.Revision); err != nil {
		t.Fatal(err)
	}
	updated := m.Snapshot()
	if len(updated.Entries) != 3 || updated.Entries[2].ID != autoID || updated.Entries[2].Content != "水" {
		t.Fatal("auto batch did not deduplicate")
	}
	invalid := ltmEntry("bad", strings.Repeat("长", 257))
	if err := m.ApplyAuto([]MemoryEntry{ltmEntry("new", "new"), invalid}, updated.Revision); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("bad auto batch: %v", err)
	}
	if !reflect.DeepEqual(updated, m.Snapshot()) {
		t.Fatal("invalid batch partially committed")
	}
	if err := m.ApplyAuto(nil, updated.Revision); err != nil || !reflect.DeepEqual(updated, m.Snapshot()) {
		t.Fatal("empty batch advanced revision")
	}
	if err := m.ApplyAuto([]MemoryEntry{ltmEntry("city", "北京")}, updated.Revision); err != nil || !reflect.DeepEqual(updated, m.Snapshot()) {
		t.Fatal("protected-only batch changed state")
	}
	replacement := ltmEntry("drink", "牛奶")
	replacement.Source = "explicit"
	replaced := ltmSave(t, m, replacement)
	if replaced.ID != autoID || replaced.Source != "explicit" {
		t.Fatal("explicit correction failed")
	}
	malicious := ltmEntry("drink", "auto overwrite")
	malicious.Source = "auto"
	if _, err := m.Save(malicious, m.Snapshot().Revision); !errors.Is(err, ErrMemoryProtected) {
		t.Fatalf("Save bypassed auto protection: %v", err)
	}
}

func TestLongTermMemoryLoadFailuresAndRecovery(t *testing.T) {
	now := time.Now().UTC()
	valid := MemoryEntry{ID: "id", Key: "city", Category: "fact", Content: "杭州", Source: "manual", CreatedAt: now, UpdatedAt: now}
	encode := func(entries []MemoryEntry) []byte {
		data, err := json.Marshal(longTermMemoryFile{Version: 1, Revision: 9, Entries: entries})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	oversized := valid
	oversized.Content = strings.Repeat("中", 257)
	tooMany := make([]MemoryEntry, 201)
	for i := range tooMany {
		tooMany[i] = valid
		tooMany[i].ID = fmt.Sprint(i)
		tooMany[i].Key = fmt.Sprint(i)
	}
	cases := map[string][]byte{
		"json":      []byte("{secret malformed"),
		"version":   []byte(`{"version":2,"entries":[]}`),
		"size":      bytes.Repeat([]byte(" "), maxMemoryFileBytes+1),
		"utf8":      []byte{0xff},
		"duplicate": encode([]MemoryEntry{valid, valid}),
		"field":     encode([]MemoryEntry{oversized}),
		"count":     encode(tooMany),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			m := OpenLongTermMemory(path)
			before := m.Snapshot()
			if before.Error == "" || before.Status != "unavailable" || len(before.Entries) != 0 || strings.Contains(before.Error, "secret") {
				t.Fatalf("bad failure snapshot: %+v", before)
			}
			if _, err := m.Save(ltmEntry("new", "new"), before.Revision); !errors.Is(err, ErrMemoryUnavailable) {
				t.Fatalf("unavailable save: %v", err)
			}
			if err := m.ApplyAuto(nil, before.Revision); !errors.Is(err, ErrMemoryUnavailable) {
				t.Fatalf("unavailable auto: %v", err)
			}
			if err := m.Delete("id", before.Revision); !errors.Is(err, ErrMemoryUnavailable) {
				t.Fatalf("unavailable delete: %v", err)
			}
			if len(m.Search("", 10)) != 0 {
				t.Fatal("unavailable search returned data")
			}
			retained, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(retained, data) {
				t.Fatal("load or rejected write rewrote bad file")
			}
			m.SetStatus("extracting")
			m.Invalidate()
			if snapshot := m.Snapshot(); snapshot.Status != "unavailable" || snapshot.Error == "" {
				t.Fatal("worker status hid unavailable state")
			}
			if err := m.Clear(m.Snapshot().Revision); err != nil {
				t.Fatal(err)
			}
			if got := OpenLongTermMemory(path).Snapshot(); got.Error != "" || len(got.Entries) != 0 {
				t.Fatalf("clear did not repair: %+v", got)
			}
		})
	}
	directory := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if snapshot := OpenLongTermMemory(directory).Snapshot(); snapshot.Error == "" {
		t.Fatal("directory accepted as memory file")
	}
}

func TestLongTermMemoryFailedWriteKeepsState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "memory.json")
	m := OpenLongTermMemory(path)
	ltmSave(t, m, ltmEntry("city", "杭州"))
	before := m.Snapshot()
	// Replace the known parent directory with a file to force a deterministic
	// mkdir failure even when tests run with elevated permissions.
	backup := dir + "-backup"
	if err := os.Rename(dir, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	operations := []func() error{
		func() error { _, err := m.Save(ltmEntry("new", "new"), before.Revision); return err },
		func() error { return m.Delete(before.Entries[0].ID, before.Revision) },
		func() error { return m.Clear(before.Revision) },
		func() error { return m.ApplyAuto([]MemoryEntry{ltmEntry("auto", "auto")}, before.Revision) },
	}
	for _, operation := range operations {
		if err := operation(); !errors.Is(err, ErrMemoryUnavailable) {
			t.Fatalf("expected disk failure, got %v", err)
		}
		snapshot := m.Snapshot()
		if snapshot.Revision != before.Revision || snapshot.Epoch != before.Epoch || !reflect.DeepEqual(snapshot.Entries, before.Entries) || snapshot.Error == "" || snapshot.Status == "unavailable" {
			t.Fatalf("failed write changed state: %+v", snapshot)
		}
		if got := m.Search("杭州", 1); len(got) != 1 {
			t.Fatal("write failure hid old data")
		}
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, dir); err != nil {
		t.Fatal(err)
	}
	ltmSave(t, m, ltmEntry("new", "new"))
	if m.Snapshot().Error != "" {
		t.Fatal("successful retry retained error")
	}
	if len(OpenLongTermMemory(path).Snapshot().Entries) != 2 {
		t.Fatal("retry did not persist")
	}
}

func TestLongTermMemoryEncodedFileLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	m := OpenLongTermMemory(path)
	before := m.Snapshot()
	entries := make([]MemoryEntry, 200)
	for i := range entries {
		entries[i] = MemoryEntry{Key: strings.Repeat("<", 75) + fmt.Sprintf("%05d", i), Category: "fact", Content: strings.Repeat("<", 256), Evidence: strings.Repeat("<", 512)}
	}
	// JSON's HTML escaping can exceed the document budget even when every
	// individual entry is within its rune limit.
	if err := m.ApplyAuto(entries, before.Revision); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("encoded size limit: %v", err)
	}
	if !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("oversized document changed memory")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized document created file: %v", err)
	}
}

func TestLongTermMemoryAtomicRenameFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	m := OpenLongTermMemory(path)
	before := m.Snapshot()
	// A nonempty directory at the target forces rename to fail after the
	// temporary file has been fully written and synced.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(path, "guard")
	if err := os.WriteFile(guard, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(ltmEntry("new", "new"), before.Revision); !errors.Is(err, ErrMemoryUnavailable) {
		t.Fatalf("rename failure: %v", err)
	}
	after := m.Snapshot()
	if after.Revision != before.Revision || after.Epoch != before.Epoch || len(after.Entries) != 0 {
		t.Fatal("rename failure published candidate")
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".long-term-memory-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files leaked: %v %v", temps, err)
	}
	if data, err := os.ReadFile(guard); err != nil || string(data) != "keep" {
		t.Fatal("failed rename damaged existing target")
	}
}

func TestLongTermMemoryFutureTimestampRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	now := time.Now().UTC().Add(time.Hour)
	entry := MemoryEntry{ID: "restored", Key: "key", Category: "fact", Content: "old", Source: "manual", CreatedAt: now, UpdatedAt: now}
	data, err := json.Marshal(longTermMemoryFile{Version: 1, Revision: 1, Entries: []MemoryEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m := OpenLongTermMemory(path)
	ltmSave(t, m, ltmEntry("key", "corrected"))
	if snapshot := OpenLongTermMemory(path).Snapshot(); snapshot.Error != "" || snapshot.Entries[0].Content != "corrected" {
		t.Fatalf("clock correction corrupted file: %+v", snapshot)
	}
}

func TestLongTermMemoryCapacityAndConcurrency(t *testing.T) {
	m := OpenLongTermMemory("")
	entries := make([]MemoryEntry, 200)
	for i := range entries {
		entries[i] = ltmEntry(fmt.Sprint(i), "value")
	}
	if err := m.ApplyAuto(entries, 0); err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	if _, err := m.Save(ltmEntry("overflow", "value"), before.Revision); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("entry capacity: %v", err)
	}
	if err := m.ApplyAuto([]MemoryEntry{ltmEntry("0", "changed"), ltmEntry("overflow", "value")}, before.Revision); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("auto capacity: %v", err)
	}
	if !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("overflow partially committed")
	}
	ltmSave(t, m, ltmEntry("0", "correction at capacity"))
	concurrent := OpenLongTermMemory("")
	var wg sync.WaitGroup
	outcomes := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			concurrent.Snapshot()
			concurrent.Search("", 10)
			_, err := concurrent.Save(ltmEntry(fmt.Sprint(i), "value"), 0)
			outcomes <- err
		}(i)
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrMemoryConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 || len(concurrent.Snapshot().Entries) != 1 {
		t.Fatalf("CAS allowed %d competing writes", successes)
	}
}

func TestSearchMemoriesChineseEnglishRanking(t *testing.T) {
	now := time.Now().UTC()
	entries := []MemoryEntry{
		{ID: "auto", Key: "language", Category: "fact", Content: "喜欢 Go 编程和杭州旅行", Source: "auto", UpdatedAt: now},
		{ID: "manual", Key: "language", Category: "fact", Content: "喜欢 Go 编程和杭州旅行", Source: "manual", UpdatedAt: now.Add(-time.Hour)},
		{ID: "explicit", Key: "language", Category: "fact", Content: "喜欢 Go 编程和杭州旅行", Source: "explicit", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "newer", Key: "misc", Category: "fact", Content: "Go programming", Source: "manual", UpdatedAt: now},
		{ID: "profile", Key: "name", Category: "profile", Content: "小明", Source: "manual", UpdatedAt: now},
		{ID: "preference", Key: "drink", Category: "preference", Content: "喝茶", Source: "manual", UpdatedAt: now},
		{ID: "extra", Key: "food", Category: "preference", Content: "面条", Source: "auto", UpdatedAt: now},
		{ID: "substring", Key: "other", Category: "fact", Content: "gopher", Source: "explicit", UpdatedAt: now},
	}
	got := SearchMemories(entries, "杭州旅行", 3)
	if len(got) != 3 || got[0].ID != "explicit" || got[1].ID != "manual" || got[2].ID != "auto" {
		t.Fatalf("Chinese/source ranking: %+v", got)
	}
	got = SearchMemories(entries, "GO", 10)
	if len(got) != 6 || got[0].ID != "explicit" || got[1].ID != "newer" || got[2].ID != "manual" || got[3].ID != "auto" {
		t.Fatalf("English/recency ranking: %+v", got)
	}
	got = SearchMemories(entries, "no matches", 10)
	if len(got) != 2 || got[0].ID != "profile" || got[1].ID != "preference" {
		t.Fatalf("bounded fallback: %+v", got)
	}
	if got := SearchMemories(entries, "杭", 1); len(got) != 1 || got[0].ID != "explicit" {
		t.Fatalf("single Han query: %+v", got)
	}
	if got := SearchMemories(entries, "", 0); len(got) != 0 {
		t.Fatal("zero limit ignored")
	}
	if got := SearchMemories(entries, "", -1); len(got) != 0 {
		t.Fatal("negative limit ignored")
	}
	if got := SearchMemories(entries, "", 999); len(got) != len(entries) {
		t.Fatal("empty query did not list entries")
	}
	large := make([]MemoryEntry, 250)
	if got := SearchMemories(large, "", 999); len(got) != 200 {
		t.Fatal("maximum result limit ignored")
	}
	stable := []MemoryEntry{{ID: "a", Content: "word"}, {ID: "b", Content: "word"}}
	if got := SearchMemories(stable, "word", 2); got[0].ID != "a" || got[1].ID != "b" {
		t.Fatal("ties not stable")
	}
}
