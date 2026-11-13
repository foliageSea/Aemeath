package voice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aemeath/agent/internal/brain"
)

func TestMemoryModeConfigurationDefaultsAndNormalization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", "auto"}, {"auto", "auto"}, {"explicit", "explicit"}, {"off", "off"},
		{" AUTO ", "auto"}, {" Explicit ", "explicit"}, {" OFF ", "off"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var cfg Config
			data, err := json.Marshal(map[string]string{"memoryMode": tc.input})
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			got, err := NormalizeConfig(cfg)
			if err != nil || got.MemoryMode != tc.want {
				t.Fatalf("memory mode=%q, err=%v; want %q", got.MemoryMode, err, tc.want)
			}
			encoded, err := json.Marshal(got)
			if err != nil || !strings.Contains(string(encoded), `"memoryMode":"`+tc.want+`"`) {
				t.Fatalf("memory mode JSON missing or invalid: %v", err)
			}
		})
	}
}

type longTermMemoryTestHost struct {
	Host
	store *brain.LongTermMemory
}

func (h longTermMemoryTestHost) LongTermMemory() *brain.LongTermMemory { return h.store }

func TestProductionBrainReceivesLongTermMemoryAndMode(t *testing.T) {
	const remembered = "unique-oolong-preference"
	store := brain.OpenLongTermMemory("")
	if _, err := store.Save(brain.MemoryEntry{Key: "tea", Category: "preference", Content: remembered, Source: "manual"}, store.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests <- string(data)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"reply","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"好的。"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name       string
		mode       string
		host       Host
		wantMemory bool
	}{
		{"explicit shared store", "explicit", longTermMemoryTestHost{store: store}, true},
		{"disabled memory", "off", longTermMemoryTestHost{store: store}, false},
		{"host without optional interface", "explicit", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, err := productionDependencies().newBrain(context.Background(), Config{LLMBaseURL: server.URL, LLMModel: "test", MemoryMode: tc.mode}, tc.host)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.(interface{ Close() error }).Close()
			if _, err := engine.Reply(context.Background(), "tea"); err != nil {
				t.Fatal(err)
			}
			request := <-requests
			if strings.Contains(request, remembered) != tc.wantMemory {
				t.Fatalf("memory store/mode injection failed: want memory=%t", tc.wantMemory)
			}
		})
	}
	if _, err := productionDependencies().newBrain(context.Background(), Config{LLMModel: "test", MemoryMode: "invalid"}, nil); err == nil {
		t.Fatal("adapter dropped invalid memory mode before brain validation")
	}
}

func TestMemoryModeConfigurationRejectsInvalidModesWithoutValues(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, mode := range []string{"manual-private-secret", "on", "auto explicit"} {
			_, err := NormalizeConfig(Config{Enabled: enabled, MemoryMode: mode})
			if err == nil {
				t.Fatal("invalid memory mode accepted")
			}
			if strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("memory validation exposed input: %v", err)
			}
		}
	}
}
