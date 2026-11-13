package holdkey

import (
	"fmt"
	"strconv"
	"strings"
)

type modifier uint8

const (
	modCtrl modifier = 1 << iota
	modShift
	modAlt
	modSuper
)

type shortcut struct {
	key       string
	modifiers modifier
}

// NormalizeShortcut validates a shortcut and returns Ctrl+Shift+Alt+Super+Key
// ordering. Key may be F1-F24, A-Z, 0-9, or Space. Letters, digits, and Space
// require Ctrl, Alt, or Super (Shift alone is insufficient). Modifier names are
// case-insensitive; Control, Option, Cmd, Command, Meta, and Win are aliases.
// Whitespace around tokens is ignored. This validates portable syntax only;
// macOS registration does not support F21-F24.
func NormalizeShortcut(value string) (string, error) {
	s, err := parseShortcut(value)
	if err != nil {
		return "", err
	}
	return s.String(), nil
}

func parseShortcut(value string) (shortcut, error) {
	var s shortcut
	for _, part := range strings.Split(value, "+") {
		token := strings.ToUpper(strings.TrimSpace(part))
		var m modifier
		switch token {
		case "CTRL", "CONTROL":
			m = modCtrl
		case "SHIFT":
			m = modShift
		case "ALT", "OPTION":
			m = modAlt
		case "SUPER", "CMD", "COMMAND", "META", "WIN":
			m = modSuper
		}
		if m != 0 {
			if s.modifiers&m != 0 {
				return shortcut{}, fmt.Errorf("holdkey: duplicate modifier %q", part)
			}
			s.modifiers |= m
			continue
		}
		if token == "" {
			return shortcut{}, fmt.Errorf("holdkey: shortcut contains an empty key or modifier")
		}
		if s.key != "" {
			return shortcut{}, fmt.Errorf("holdkey: shortcut must contain exactly one main key")
		}
		switch {
		case token == "SPACE":
			s.key = "Space"
		case len(token) == 1 && ((token[0] >= 'A' && token[0] <= 'Z') || (token[0] >= '0' && token[0] <= '9')):
			s.key = token
		case strings.HasPrefix(token, "F"):
			n, err := strconv.Atoi(token[1:])
			if err != nil || n < 1 || n > 24 || token != "F"+strconv.Itoa(n) {
				return shortcut{}, fmt.Errorf("holdkey: unsupported key %q (expected F1-F24, A-Z, 0-9, or Space)", part)
			}
			s.key = token
		default:
			return shortcut{}, fmt.Errorf("holdkey: unsupported key %q (expected F1-F24, A-Z, 0-9, or Space)", part)
		}
	}
	if s.key == "" {
		return shortcut{}, fmt.Errorf("holdkey: shortcut requires a main key")
	}
	if !(len(s.key) > 1 && strings.HasPrefix(s.key, "F")) && s.modifiers&(modCtrl|modAlt|modSuper) == 0 {
		return shortcut{}, fmt.Errorf("holdkey: letters, digits, and Space require Ctrl, Alt, or Super")
	}
	return s, nil
}

func (s shortcut) String() string {
	var parts []string
	for _, entry := range []struct {
		bit  modifier
		name string
	}{{modCtrl, "Ctrl"}, {modShift, "Shift"}, {modAlt, "Alt"}, {modSuper, "Super"}} {
		if s.modifiers&entry.bit != 0 {
			parts = append(parts, entry.name)
		}
	}
	return strings.Join(append(parts, s.key), "+")
}

// windowsCodes returns a Win32 virtual key and RegisterHotKey modifier mask.
// No-repeat is added by the native listener, not by this mapping.
func (s shortcut) windowsCodes() (key, modifiers uint32) {
	switch {
	case s.key == "Space":
		key = 0x20
	case len(s.key) == 1:
		key = uint32(s.key[0])
	default:
		n, _ := strconv.Atoi(s.key[1:])
		key = 0x70 + uint32(n-1)
	}
	for _, entry := range []struct {
		bit  modifier
		code uint32
	}{{modCtrl, 2}, {modShift, 4}, {modAlt, 1}, {modSuper, 8}} {
		if s.modifiers&entry.bit != 0 {
			modifiers |= entry.code
		}
	}
	return
}

// carbonCodes uses Carbon physical ANSI key positions, independent of the
// active keyboard layout (letters/digits refer to US ANSI key positions).
func (s shortcut) carbonCodes() (key, modifiers uint32, err error) {
	var ok bool
	key, ok = carbonKeyCodes[s.key]
	if !ok {
		return 0, 0, fmt.Errorf("holdkey: %s is unsupported on macOS (F1-F20 supported)", s.key)
	}
	for _, entry := range []struct {
		bit  modifier
		code uint32
	}{{modCtrl, 1 << 12}, {modShift, 1 << 9}, {modAlt, 1 << 11}, {modSuper, 1 << 8}} {
		if s.modifiers&entry.bit != 0 {
			modifiers |= entry.code
		}
	}
	return key, modifiers, nil
}

var carbonKeyCodes = map[string]uint32{
	"A": 0, "S": 1, "D": 2, "F": 3, "H": 4, "G": 5, "Z": 6, "X": 7, "C": 8, "V": 9,
	"B": 11, "Q": 12, "W": 13, "E": 14, "R": 15, "Y": 16, "T": 17,
	"1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "9": 25, "7": 26, "8": 28, "0": 29,
	"O": 31, "U": 32, "I": 34, "P": 35, "L": 37, "J": 38, "K": 40, "N": 45, "M": 46, "Space": 49,
	"F1": 122, "F2": 120, "F3": 99, "F4": 118, "F5": 96, "F6": 97, "F7": 98, "F8": 100,
	"F9": 101, "F10": 109, "F11": 103, "F12": 111, "F13": 105, "F14": 107, "F15": 113,
	"F16": 106, "F17": 64, "F18": 79, "F19": 80, "F20": 90,
}

// windowsHeld checks every required component, so releasing a modifier while
// the main key remains down ends the hold too. Either side of a modifier works.
func (s shortcut) windowsHeld(isDown func(uint32) bool) bool {
	key, _ := s.windowsCodes()
	if !isDown(key) {
		return false
	}
	for _, entry := range []struct {
		bit         modifier
		left, right uint32
	}{
		{modCtrl, 0xA2, 0xA3}, {modShift, 0xA0, 0xA1}, {modAlt, 0xA4, 0xA5}, {modSuper, 0x5B, 0x5C},
	} {
		if s.modifiers&entry.bit != 0 && !isDown(entry.left) && !isDown(entry.right) {
			return false
		}
	}
	return true
}
