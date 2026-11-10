package desktop

import (
	"path/filepath"
	"testing"
)

func TestReloadWakeModelsOnlyRestartsSavedEnabledDirectory(t *testing.T) {
	s := testService(t, testDeps())
	target := filepath.Join(t.TempDir(), "models")
	cancellations := 0
	s.mu.Lock()
	s.state.Settings.Voice.Enabled = true
	s.state.Settings.Voice.WakeModelDir = target
	s.attemptCancel = func() { cancellations++ }
	s.mu.Unlock()
	before := s.Snapshot().Settings
	if err := s.ReloadWakeModels(filepath.Join(t.TempDir(), "draft-models")); err != nil {
		t.Fatal(err)
	}
	if cancellations != 0 {
		t.Fatal("draft path restarted saved configuration")
	}
	if err := s.ReloadWakeModels(target); err != nil {
		t.Fatal(err)
	}
	if cancellations != 1 {
		t.Fatal("newly installed saved models not reloaded")
	}
	if got := s.Snapshot().Settings; got.Voice != before.Voice || got.Address != before.Address {
		t.Fatal("reload modified settings")
	}
	s.mu.Lock()
	s.state.Settings.Voice.Enabled = false
	s.mu.Unlock()
	if err := s.ReloadWakeModels(target); err != nil {
		t.Fatal(err)
	}
	if cancellations != 1 {
		t.Fatal("disabled voice configuration restarted")
	}
}
