package pet

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestConfigDefaultsPartialAndNoIgnoreMouse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pet.json")
	c, err := LoadConfig(path)
	if err != nil || c != DefaultConfig() {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("load created config")
	}
	if err = os.WriteFile(path, []byte(`{"visible":false,"ignoreMouse":true,"scale":1.25,"positionSet":true,"x":0,"y":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Visible || !c.AlwaysOnTop || !c.Wander || c.Scale != 1.25 || !c.PositionSet || c.X != 0 {
		t.Fatalf("bad defaults: %+v", c)
	}
	if err = SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(b)), "ignoremouse") {
		t.Fatal("unsafe input setting persisted")
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded != c {
		t.Fatalf("roundtrip: %+v %v", loaded, err)
	}
}
func TestConfigNormalize(t *testing.T) {
	for _, scale := range []float64{.75, 1, 1.25, 1.5} {
		c := NormalizeConfig(Config{Scale: scale})
		if c.Scale != scale {
			t.Fatal(scale)
		}
	}
	for _, scale := range []float64{0, -1, 9, .8, math.NaN(), math.Inf(1)} {
		c := NormalizeConfig(Config{Scale: scale, Frequency: "invalid"})
		if c.Scale != 1 || c.Frequency != Standard {
			t.Fatal(c)
		}
	}
	c := NormalizeConfig(Config{PositionSet: true, X: math.NaN(), Y: 1})
	if c.PositionSet || c.X != 0 || c.Y != 0 {
		t.Fatal(c)
	}
	c = NormalizeConfig(Config{PositionSet: true, X: -1e9, Y: 1e9})
	if c.X != -1e6 || c.Y != 1e6 || !c.PositionSet {
		t.Fatal(c)
	}
	for _, f := range []Frequency{Slow, Standard, Fast} {
		if NormalizeConfig(Config{Frequency: f}).Frequency != f {
			t.Fatal(f)
		}
	}
}
func TestConfigAtomicPermissionsAndIsolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pet.json")
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte("main settings unchanged"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(path, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	b, err := os.ReadFile(settings)
	if err != nil || string(b) != "main settings unchanged" {
		t.Fatal("main config changed")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".pet-*.tmp"))
	if len(leftovers) != 0 {
		t.Fatal(leftovers)
	}
	nested := filepath.Join(dir, "new", "nested", "pet.json")
	if err = SaveConfig(nested, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
}
func TestConfigBadFilesReturnDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pet.json")
	for _, text := range []string{"", "{", "null", "[]", "{} {}", `{"scale":"large"}`, strings.Repeat(" ", 65537)} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadConfig(path)
		if err == nil || c != DefaultConfig() {
			t.Fatalf("accepted malformed config %q: %+v %v", text[:min(len(text), 32)], c, err)
		}
	}
}
func TestConfigFailedSaveCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, DefaultConfig()); err == nil {
		t.Fatal("saved over directory")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".pet-*.tmp"))
	if len(files) != 0 {
		t.Fatal(files)
	}
}
func TestConfigConcurrentWritesRemainComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pet.json")
	if err := SaveConfig(path, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				c := DefaultConfig()
				c.X = float64(i*100 + j)
				if err := SaveConfig(path, c); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	for i := 0; i < 40; i++ {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c Config
		if err = json.Unmarshal(b, &c); err != nil {
			t.Fatalf("partial atomic file: %s", b)
		}
	}
	wg.Wait()
}
