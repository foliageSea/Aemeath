package voice

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	protocolVersion = 1
	sampleRate      = 16000
	frameSamples    = 320
	headerSize      = 20
	kindMicrophone  = 1
	kindPlayback    = 2
	maxTextBytes    = 16 * 1024
	maxCredits      = 64
)

type audioFrame struct {
	Kind             uint8
	Epoch, Turn, Seq uint32
	PCM              []int16
}

func decodeAudio(b []byte) (audioFrame, error) {
	var f audioFrame
	if len(b) < headerSize || string(b[:4]) != "AEAU" || b[4] != protocolVersion || binary.LittleEndian.Uint16(b[6:8]) != 0 {
		return f, errors.New("invalid audio header")
	}
	n := len(b) - headerSize
	if n == 0 || n%2 != 0 || n > frameSamples*2 {
		return f, errors.New("invalid audio payload length")
	}
	f.Kind = b[5]
	f.Epoch = binary.LittleEndian.Uint32(b[8:12])
	f.Turn = binary.LittleEndian.Uint32(b[12:16])
	f.Seq = binary.LittleEndian.Uint32(b[16:20])
	if f.Epoch == 0 || (f.Kind != kindMicrophone && f.Kind != kindPlayback) || (f.Kind == kindMicrophone && (n != frameSamples*2 || f.Turn != 0)) || (f.Kind == kindPlayback && f.Turn == 0) {
		return audioFrame{}, errors.New("invalid audio metadata")
	}
	f.PCM = make([]int16, n/2)
	for i := range f.PCM {
		f.PCM[i] = int16(binary.LittleEndian.Uint16(b[headerSize+i*2:]))
	}
	return f, nil
}

func encodeAudio(f audioFrame) []byte {
	b := make([]byte, headerSize+len(f.PCM)*2)
	copy(b, "AEAU")
	b[4] = protocolVersion
	b[5] = f.Kind
	binary.LittleEndian.PutUint32(b[8:], f.Epoch)
	binary.LittleEndian.PutUint32(b[12:], f.Turn)
	binary.LittleEndian.PutUint32(b[16:], f.Seq)
	for i, v := range f.PCM {
		binary.LittleEndian.PutUint16(b[headerSize+i*2:], uint16(v))
	}
	return b
}

type message struct {
	V            int      `json:"v"`
	Type         string   `json:"type"`
	Epoch        uint32   `json:"epoch"`
	Turn         uint32   `json:"turn"`
	SampleRate   int      `json:"sample_rate,omitempty"`
	Channels     int      `json:"channels,omitempty"`
	Encoding     string   `json:"encoding,omitempty"`
	FrameSamples int      `json:"frame_samples,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Enabled      bool     `json:"enabled,omitempty"`
	Volume       int      `json:"volume"`
	State        string   `json:"state,omitempty"`
	Text         string   `json:"text,omitempty"`
	Samples      int      `json:"samples,omitempty"`
	Streaming    bool     `json:"streaming,omitempty"`
	Frames       int      `json:"frames,omitempty"`
	Muted        bool     `json:"muted,omitempty"`
	Code         string   `json:"code,omitempty"`
	Message      string   `json:"message,omitempty"`
}

func decodeMessage(b []byte) (message, error) {
	var m message
	if len(b) > maxTextBytes {
		return m, errors.New("voice control message too large")
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, errors.New("invalid voice control JSON")
	}
	if m.V != protocolVersion || m.Epoch == 0 || m.Type == "" {
		return m, errors.New("invalid voice control header")
	}
	return m, nil
}

func (m message) validateHello() error {
	if m.Type != "hello" || m.SampleRate != sampleRate || m.Channels != 1 || m.Encoding != "pcm_s16le" || m.FrameSamples != frameSamples {
		return fmt.Errorf("unsupported device voice format")
	}
	for _, need := range []string{"capture", "playback"} {
		found := false
		for _, capability := range m.Capabilities {
			if capability == need {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("device missing %s capability", need)
		}
	}
	return nil
}
