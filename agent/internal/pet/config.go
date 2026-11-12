package pet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

type Frequency string

const (
	Slow     Frequency = "slow"
	Standard Frequency = "standard"
	Fast     Frequency = "fast"
)

// Config is stored independently from desktop settings. Mouse pass-through is
// deliberately absent: every process launch must start with mouse input enabled.
type Config struct {
	Visible     bool      `json:"visible"`
	AlwaysOnTop bool      `json:"alwaysOnTop"`
	Wander      bool      `json:"wander"`
	Scale       float64   `json:"scale"`
	Frequency   Frequency `json:"frequency"`
	PositionSet bool      `json:"positionSet"`
	X           float64   `json:"x"`
	Y           float64   `json:"y"`
}

func DefaultConfig() Config {
	return Config{Visible: true, AlwaysOnTop: true, Wander: true, Scale: 1, Frequency: Standard}
}

// NormalizeConfig preserves explicit false booleans. Missing JSON fields are
// defaulted by LoadConfig; a Config zero value is not the same as DefaultConfig.
func NormalizeConfig(c Config) Config {
	switch c.Scale {
	case .75, 1, 1.25, 1.5:
	default:
		c.Scale = 1
	}
	switch c.Frequency {
	case Slow, Standard, Fast:
	default:
		c.Frequency = Standard
	}
	if !finite(c.X) || !finite(c.Y) {
		c.PositionSet = false
		c.X = 0
		c.Y = 0
	}
	c.X = math.Max(-1e6, math.Min(1e6, c.X))
	c.Y = math.Max(-1e6, math.Min(1e6, c.Y))
	return c
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// LoadConfig returns defaults when path does not exist, without creating a file.
// Unknown keys (including obsolete ignoreMouse) are ignored and never re-saved.
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return c, err
	}
	if len(b) > 65536 {
		return c, errors.New("pet config exceeds 64 KiB")
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return c, errors.New("pet config must be an object")
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return DefaultConfig(), fmt.Errorf("pet config: %w", err)
	}
	return NormalizeConfig(c), nil
}

// SaveConfig writes only pet preferences, with a same-directory atomic rename.
// The temporary and final file are mode 0600; no service settings are touched.
func SaveConfig(path string, c Config) (err error) {
	c = NormalizeConfig(c)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pet-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer func() { f.Close(); os.Remove(temp) }()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	return nil
}
