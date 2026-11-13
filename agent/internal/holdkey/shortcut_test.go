package holdkey

import (
	"errors"
	"fmt"
	"testing"
)

func TestNormalizeShortcut(t *testing.T) {
	cases := map[string]string{
		"f8": "F8", " shift + f24 ": "Shift+F24", "option + Control + cmd + SHIFT + space": "Ctrl+Shift+Alt+Super+Space",
		"command+a": "Super+A", "meta+z": "Super+Z", "win+0": "Super+0", "Super+f": "Super+F",
		"a + alt": "Alt+A", "\tcontrol\t+\n9\n": "Ctrl+9", "Alt+Shift+F1": "Shift+Alt+F1",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := NormalizeShortcut(input)
			if err != nil || got != want {
				t.Fatalf("NormalizeShortcut(%q) = (%q, %v), want %q", input, got, err, want)
			}
			again, err := NormalizeShortcut(got)
			if err != nil || again != got {
				t.Fatalf("normalization not idempotent: %q, %v", again, err)
			}
		})
	}
	for n := 1; n <= 24; n++ {
		input := fmt.Sprintf("F%d", n)
		if got, err := NormalizeShortcut(input); err != nil || got != input {
			t.Fatalf("%s: %q %v", input, got, err)
		}
	}
	for _, key := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" {
		for _, prefix := range []string{"Ctrl+", "Alt+", "Super+", "Ctrl+Shift+Alt+Super+"} {
			input := prefix + string(key)
			if got, err := NormalizeShortcut(input); err != nil || got != input {
				t.Fatalf("%s: %q %v", input, got, err)
			}
		}
	}
}

func TestShortcutValidation(t *testing.T) {
	invalid := []string{"", " ", "Ctrl", "Ctrl+Shift", "Super+Alt", "+F8", "F8+", "Ctrl++F8", "Ctrl+ +F8",
		"Ctrl+Ctrl+F8", "Ctrl+Control+A", "Cmd+Meta+F8", "Super+Win+F8", "Alt+Option+Space", "Shift+shift+F1",
		"F8+F9", "Ctrl+A+B", "F0", "F25", "F01", "F-1", "F1.0", "Ctrl+Enter", "Ctrl+Escape", "Ctrl+é", "Ctrl+中", "Ctrl+F 8",
		"A", "F", "0", "Space", "Shift+A", "Shift+F", "Shift+9", "Shift+Space"}
	for _, input := range invalid {
		t.Run(input, func(t *testing.T) {
			if got, err := NormalizeShortcut(input); err == nil || got != "" {
				t.Fatalf("NormalizeShortcut(%q) = (%q, %v), want empty and error", input, got, err)
			}
			stop, err := StartShortcut(input, func(bool) { t.Error("invalid shortcut invoked callback") })
			if err == nil || stop != nil || errors.Is(err, ErrUnsupported) {
				t.Fatalf("StartShortcut(%q) = (%v,%v), want validation error before native API", input, stop != nil, err)
			}
		})
	}
}

func TestNilCallbacks(t *testing.T) {
	for name, start := range map[string]func(func(bool)) (func(), error){"Start": Start, "StartAlternate": StartAlternate, "StartShortcut": func(cb func(bool)) (func(), error) { return StartShortcut("Ctrl+Space", cb) }} {
		t.Run(name, func(t *testing.T) {
			stop, err := start(nil)
			if stop != nil || err == nil || errors.Is(err, ErrUnsupported) {
				t.Fatalf("nil callback = (%v,%v), want validation error", stop != nil, err)
			}
		})
	}
}

func mustParse(t *testing.T, value string) shortcut {
	t.Helper()
	s, err := parseShortcut(value)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNativeMappings(t *testing.T) {
	cases := []struct {
		input                                string
		winKey, winMod, carbonKey, carbonMod uint32
	}{
		{"F8", 0x77, 0, 100, 0}, {"Ctrl+Shift+F8", 0x77, 6, 100, 0x1200},
		{"Alt+Space", 0x20, 1, 49, 0x800}, {"Super+A", 0x41, 8, 0, 0x100},
		{"Ctrl+Shift+Alt+Super+9", 0x39, 15, 25, 0x1B00}, {"Ctrl+F", 0x46, 2, 3, 0x1000},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			s := mustParse(t, tc.input)
			k, m := s.windowsCodes()
			if k != tc.winKey || m != tc.winMod {
				t.Errorf("Windows=(%#x,%#x), want (%#x,%#x)", k, m, tc.winKey, tc.winMod)
			}
			k, m, err := s.carbonCodes()
			if err != nil || k != tc.carbonKey || m != tc.carbonMod {
				t.Errorf("Carbon=(%d,%#x,%v), want (%d,%#x,nil)", k, m, err, tc.carbonKey, tc.carbonMod)
			}
		})
	}
	carbonFunctions := []uint32{122, 120, 99, 118, 96, 97, 98, 100, 101, 109, 103, 111, 105, 107, 113, 106, 64, 79, 80, 90}
	for n := 1; n <= 24; n++ {
		s := mustParse(t, fmt.Sprintf("F%d", n))
		k, m := s.windowsCodes()
		if k != uint32(0x6F+n) || m != 0 {
			t.Errorf("F%d Windows=(%#x,%#x)", n, k, m)
		}
		k, m, err := s.carbonCodes()
		if n <= 20 {
			if err != nil || k != carbonFunctions[n-1] || m != 0 {
				t.Errorf("F%d Carbon=(%d,%#x,%v)", n, k, m, err)
			}
		} else if err == nil {
			t.Errorf("F%d must be explicitly rejected on macOS", n)
		}
	}
	// Check every ANSI letter/digit mapping, including valid zero keycode A.
	letters := []uint32{0, 11, 8, 2, 14, 3, 5, 4, 34, 38, 40, 37, 46, 45, 31, 35, 12, 15, 1, 17, 32, 9, 13, 7, 16, 6}
	digits := []uint32{29, 18, 19, 20, 21, 23, 22, 26, 28, 25}
	for i, r := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" {
		s := mustParse(t, "Ctrl+"+string(r))
		wk, wm := s.windowsCodes()
		ck, cm, err := s.carbonCodes()
		want := uint32(0)
		if i < 26 {
			want = letters[i]
		} else {
			want = digits[i-26]
		}
		if wk != uint32(r) || wm != 2 || ck != want || cm != 0x1000 || err != nil {
			t.Errorf("%c mapping Windows=(%d,%d), Carbon=(%d,%d,%v)", r, wk, wm, ck, cm, err)
		}
	}
}

func TestWindowsReleaseComponents(t *testing.T) {
	s := mustParse(t, "Ctrl+Shift+Alt+Super+Space")
	for _, keys := range [][]uint32{{0x20, 0xA2, 0xA0, 0xA4, 0x5B}, {0x20, 0xA3, 0xA1, 0xA5, 0x5C}} {
		held := map[uint32]bool{}
		for _, key := range keys {
			held[key] = true
		}
		isDown := func(key uint32) bool { return held[key] }
		if !s.windowsHeld(isDown) {
			t.Fatal("complete chord should be held")
		}
		for _, key := range keys {
			held[key] = false
			if s.windowsHeld(isDown) {
				t.Errorf("release of %#x did not end hold", key)
			}
			held[key] = true
		}
	}
	bare := mustParse(t, "F8")
	if !bare.windowsHeld(func(key uint32) bool { return key == 0x77 }) {
		t.Fatal("bare function key requires no modifiers")
	}
	if bare.windowsHeld(func(uint32) bool { return false }) {
		t.Fatal("released main key remains held")
	}
	// Releasing one side does not release a modifier still held on the other side.
	ctrl := mustParse(t, "Ctrl+A")
	if !ctrl.windowsHeld(func(key uint32) bool { return key == 0x41 || key == 0xA3 }) {
		t.Fatal("right Control not recognized")
	}
}
