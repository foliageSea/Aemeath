package voice

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
)

func TestAudioCodecAndValidation(t *testing.T) {
	pcm := make([]int16, frameSamples)
	pcm[0] = -32768
	pcm[319] = 32767
	want := audioFrame{Kind: kindMicrophone, Epoch: 42, Seq: 17, PCM: pcm}
	b := encodeAudio(want)
	got, err := decodeAudio(b)
	if err != nil || got.Epoch != 42 || got.Seq != 17 || got.PCM[0] != -32768 || got.PCM[319] != 32767 {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:19] }, func(b []byte) []byte { return b[:len(b)-1] }, func(b []byte) []byte { b[4] = 2; return b },
		func(b []byte) []byte { b[6] = 1; return b }, func(b []byte) []byte { b[5] = 7; return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[12:], 1); return b }, func(b []byte) []byte { return append(b, 0, 0) },
	} {
		if _, err := decodeAudio(mutate(append([]byte{}, b...))); err == nil {
			t.Fatal("accepted invalid audio")
		}
	}
	last := encodeAudio(audioFrame{Kind: kindPlayback, Epoch: 42, Turn: 1, PCM: []int16{9}})
	if _, err := decodeAudio(last); err != nil {
		t.Fatal(err)
	}
}
func TestNormalizeDeferredConfiguration(t *testing.T) {
	c, err := NormalizeConfig(Config{Enabled: true})
	if err != nil || c.ReferenceAudio == "" || c.WakeModelDir == "" || c.LLMAPIKey != "" {
		t.Fatalf("defaults require no credentials/assets: %+v %v", c, err)
	}
	if _, err = NormalizeConfig(Config{LLMBaseURL: "broken", WakeThreshold: math.NaN(), Volume: -2}); err != nil {
		t.Fatal("disabled config rejected", err)
	}
	for _, c := range []Config{{Enabled: true, LLMBaseURL: "ftp://test"}, {Enabled: true, MiMoBaseURL: "https://secret@example.com"}, {Enabled: true, Volume: 101}, {Enabled: true, WakeThreshold: math.NaN()}} {
		if _, err := NormalizeConfig(c); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	b, _ := json.Marshal(message{V: 1, Type: "config", Epoch: 42, Enabled: true, Volume: 0})
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, ok := m["volume"]; !ok {
		t.Fatal("intentional zero volume omitted")
	}
	for _, address := range []string{"localhost:1234", "ws://localhost:1234", "wss://localhost/voice"} {
		if _, err := voiceURL(address); err != nil {
			t.Fatal(err)
		}
	}
	for _, address := range []string{"http://localhost", "ws://user:secret@localhost", "ws://localhost/ws", ""} {
		if _, err := voiceURL(address); err == nil {
			t.Fatal("accepted bad address", address)
		}
	}
}
func TestCaptureBoundsAndSuffixRing(t *testing.T) {
	var ring sampleRing
	for i := 0; i < preRollSamples+100; i++ {
		ring.add([]int16{int16(i)})
	}
	tail := ring.tail(10)
	if len(tail) != 10 || tail[9] != int16(preRollSamples+99) {
		t.Fatal("wrong ring suffix", tail)
	}
	if len(ring.tail(preRollSamples+10)) != preRollSamples {
		t.Fatal("unbounded ring")
	}
	var c capture
	frame := make([]int16, frameSamples)
	for i := 0; i < 249; i++ {
		if done, _ := c.feed(frame, false); done {
			t.Fatal("early timeout")
		}
	}
	if len(c.pcm) > speechLeadSamples {
		t.Fatal("unbounded waiting capture")
	}
	if done, empty := c.feed(frame, false); !done || !empty {
		t.Fatal("missing silence timeout")
	}
	c = capture{}
	for i := 0; i < 1499; i++ {
		if done, _ := c.feed(frame, true); done {
			t.Fatal("early max")
		}
	}
	if done, empty := c.feed(frame, true); !done || empty || len(c.pcm) != maxUtteranceSamples {
		t.Fatal("utterance not bounded")
	}
	c = capture{}
	c.feed(frame, true)
	for i := 0; i < 39; i++ {
		if done, _ := c.feed(frame, false); done {
			t.Fatal("early silence end")
		}
	}
	if done, empty := c.feed(frame, false); !done || empty {
		t.Fatal("missing silence endpoint")
	}
}
