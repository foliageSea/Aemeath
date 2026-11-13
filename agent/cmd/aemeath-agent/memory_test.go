package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
)

func TestCLIMemoryFileDefaultAndExplicitPaths(t *testing.T) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"default", nil, filepath.Join(configDir, "Aemeath", "cli_memory.json")},
		{"standalone default", []string{"--standalone"}, filepath.Join(configDir, "Aemeath", "cli_memory.json")},
		{"device custom", []string{"--voice", "--memory-file", "relative memory.json"}, "relative memory.json"},
		{"standalone custom", []string{"--standalone", "--memory-file=/tmp/aemeath-memory.json"}, "/tmp/aemeath-memory.json"},
		{"explicit process memory", []string{"--memory-file="}, ""},
		{"standalone process memory", []string{"--standalone", "--memory-file", ""}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseArgs(tc.args, io.Discard)
			if err != nil || opts.memoryFile != tc.want {
				t.Fatalf("memory file=%q, err=%v; want %q", opts.memoryFile, err, tc.want)
			}
		})
	}
}

func TestCLIMemoryFileWithoutUserConfigDirectory(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "freebsd", "openbsd", "netbsd", "windows":
	default:
		t.Skip("platform has a fixed user configuration directory")
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "AppData"} {
		t.Setenv(key, "")
	}
	_, err := parseArgs(nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--memory-file") {
		t.Fatalf("missing user configuration directory: %v", err)
	}
	for _, arg := range []string{"--memory-file=", "--memory-file=private-path"} {
		if _, err := parseArgs([]string{arg}, io.Discard); err != nil {
			t.Fatalf("explicit memory file must bypass default lookup: %v", err)
		}
	}
}

func TestCLIMemoryFileHelp(t *testing.T) {
	var output bytes.Buffer
	_, err := parseArgs([]string{"--help"}, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	for _, want := range []string{"memory-file", "cli_memory.json", "empty", "process memory"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("memory file help missing %q", want)
		}
	}
}

func TestCLIVoiceConfigMemoryMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.json")
	if err := os.WriteFile(path, []byte(`{"memoryMode":" EXPLICIT "}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadVoiceConfig(path, false)
	if err != nil || cfg.MemoryMode != "explicit" {
		t.Fatalf("memory mode=%q, err=%v", cfg.MemoryMode, err)
	}
}

func TestCLIExplicitMemoryRequestsUseAgentText(t *testing.T) {
	commands := make(chan voice.Command, 8)
	control := &cliVoiceControl{commands: commands, connected: true}
	control.console(context.Background(), strings.NewReader("remember private-memory\nrecall private-memory\nforget private-memory\ntext 请记住我喜欢茶。\n"))
	if len(commands) != 1 {
		t.Fatalf("memory operations bypassed Agent text: %d commands", len(commands))
	}
	if got := <-commands; got.Kind != "text" || got.Text != "请记住我喜欢茶。" {
		t.Fatalf("unexpected memory request: %+v", got)
	}
}

func TestCLIHostsShareLongTermMemoryAcrossReconnects(t *testing.T) {
	store := brain.OpenLongTermMemory("")
	device := &cliVoiceHost{longTermMemory: store, memory: brain.OpenMemory("")}
	var first voice.LongTermMemoryHost = device
	entry, err := first.LongTermMemory().Save(brain.MemoryEntry{
		Key: "drink", Category: "preference", Content: "喜欢茶", Source: "explicit", Evidence: "请记住我喜欢茶。",
	}, store.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	// Reconnects replace hosts and conversation engines but retain the store.
	for _, host := range []*cliVoiceHost{{longTermMemory: store}, {longTermMemory: store}} {
		if host.LongTermMemory() != store {
			t.Fatal("host replaced shared long-term store")
		}
		entries := host.LongTermMemory().Snapshot().Entries
		if len(entries) != 1 || entries[0].ID != entry.ID {
			t.Fatal("reconnected/standalone host lost remembered preference")
		}
	}
	device.ConversationMemory().Clear()
	if len(store.Snapshot().Entries) != 1 {
		t.Fatal("clearing conversation removed long-term memory")
	}
}
