package voice

import (
	"context"
	"strings"
	"testing"
)

func TestSpeechTextFilterChunkBoundaries(t *testing.T) {
	cases := []struct{ input, want string }{
		{"东莞今天（11月10日）多云，气温约17～25°C。[1]\n[1] IP.cn《东莞2026年11月份历史天气》", "东莞今天（11月10日）多云，气温约17～25°C。\n"},
		{"正文[1][12]继续。\r\n  [1] 来源标题\r\n[12] https://example.com\n下一段正文。", "正文继续。\r\n  \n下一段正文。"},
		{"普通[说明]，数组[1, 2]，区间[1-3]和未闭合[12", "普通[说明]，数组[1, 2]，区间[1-3]和未闭合[12"},
		{"1. 第一步\n2. 第二步\n[备注] 保留。", "1. 第一步\n2. 第二步\n[备注] 保留。"},
		{"[1] 只有来源", ""},
	}
	for _, tc := range cases {
		for split := 0; split <= len(tc.input); split++ {
			var f speechTextFilter
			got := f.Push(tc.input[:split]) + f.Push(tc.input[split:]) + f.Finish()
			if got != tc.want {
				t.Fatalf("split %d: got %q, want %q", split, got, tc.want)
			}
		}
		var f speechTextFilter
		var got strings.Builder
		for i := range len(tc.input) {
			got.WriteString(f.Push(tc.input[i : i+1]))
		}
		got.WriteString(f.Finish())
		if got.String() != tc.want {
			t.Fatalf("byte stream: got %q, want %q", got.String(), tc.want)
		}
	}
}

func TestPipelineCitationsOnlyExcludedFromSpeech(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "wav"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			response := "东莞今天（11月10日）多云，气温约17～25°C。[1]\n[1] IP.cn《东莞2026年11月份历史天气》"
			engine := &pipelineTestBrain{stream: func(_ context.Context, _ string, delta func(string) error) (string, error) {
				for i := range len(response) {
					if err := delta(response[i : i+1]); err != nil {
						return "", err
					}
				}
				return response, nil
			}}
			var spoken []string
			var speech speechAPI = &pipelineTestSpeech{synth: func(_ context.Context, text, _ string, _ int) ([]int16, error) {
				spoken = append(spoken, text)
				return []int16{1}, nil
			}}
			if streaming {
				speech = &streamTestSpeech{stream: func(_ context.Context, parts <-chan string, audio func([]int16) error) error {
					for part := range parts {
						spoken = append(spoken, strings.TrimSpace(part))
					}
					return audio([]int16{1})
				}}
			}
			var events pipelineTestEvents
			if err := segmentedSpeech(pipelineTestContext(t), engine, speech, "天气", "", events.emit); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(spoken, ""); got != "东莞今天（11月10日）多云，气温约17～25°C。" {
				t.Fatalf("speech: %q", got)
			}
			var displayed string
			for _, event := range events.snapshot() {
				if event.phase == "response" {
					displayed = event.text
				}
			}
			if displayed != response {
				t.Fatalf("display lost citations: %q", displayed)
			}
		})
	}
}
