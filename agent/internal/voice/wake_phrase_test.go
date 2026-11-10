package voice

import (
	"context"
	"testing"
	"time"
)

func TestCustomWakePhraseConfigAndPrefix(t *testing.T) {
	cfg, err := NormalizeConfig(Config{WakePhrase: " 小智同学 "})
	if err != nil || cfg.WakePhrase != "小智同学" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	defaults, err := NormalizeConfig(Config{})
	if err != nil || defaults.WakePhrase != "你好小爱" {
		t.Fatalf("legacy config=%+v err=%v", defaults, err)
	}
	if _, err := NormalizeConfig(Config{WakePhrase: "invalid"}); err == nil {
		t.Fatal("invalid phrase accepted even with disabled voice")
	}
	for _, tc := range []struct{ in, phrase, want string }{
		{"小智，同学，打开空调。", "小智同学", "打开空调。"},
		{" 小智同学！ ", "小智同学", ""},
		{"请解释小智同学的意思", "小智同学", "请解释小智同学的意思"},
		{"你好小爱，打开空调", "小智同学", "你好小爱，打开空调"},
		{"小智同学，打开空调", "你好小爱", "小智同学，打开空调"},
		{"你好，小爱，打开空调", "", "打开空调"},
	} {
		if got := stripWakePhrasePrefix(tc.in, tc.phrase); got != tc.want {
			t.Errorf("strip(%q,%q)=%q want=%q", tc.in, tc.phrase, got, tc.want)
		}
	}
}

func TestWorkerUsesCustomWakePhrase(t *testing.T) {
	for _, tc := range []struct{ asr, phase, text string }{
		{"小智，同学！", "wake_only", ""},
		{"小智同学，打开空调", "transcript", "打开空调"},
		{"你好小爱，打开空调", "transcript", "你好小爱，打开空调"},
	} {
		t.Run(tc.asr, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			deps := fakeDependencies()
			deps.newSpeech = func(Config) speechAPI {
				return &fakeSpeech{recognize: func(context.Context, []int16) (string, error) { return tc.asr, nil }}
			}
			jobs := make(chan work, 1)
			out := make(chan result, 8)
			jobs <- work{ctx: ctx, kind: "utterance", turn: 1, pcm: make([]int16, 320)}
			// No model is configured: the transcript must be observable before
			// generation fails, with no need for a cloud request or device action.
			done := make(chan struct{})
			go func() {
				defer close(done)
				worker(ctx, Config{WakePhrase: "小智同学", MiMoAPIKey: "test-key"}, nil, deps, jobs, out)
			}()
			defer func() { cancel(); <-done }()
			for {
				select {
				case r := <-out:
					if r.phase == tc.phase {
						if r.text != tc.text {
							t.Fatalf("text=%q want=%q", r.text, tc.text)
						}
						return
					}
					if r.err != nil {
						t.Fatalf("failed before %s: %v", tc.phase, r.err)
					}
				case <-ctx.Done():
					t.Fatal("worker did not publish custom wake result")
				}
			}
		})
	}
}
