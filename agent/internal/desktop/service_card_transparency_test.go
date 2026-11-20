package desktop

import "testing"

func TestCardTransparencyUsesDeviceReports(t *testing.T) {
	s, conn := pageStateService(t)
	defer s.Close()
	const screen = `{"v":1,"type":"screen_state","on":false,"brightness":42`
	s.handleDeviceFrame(conn, []byte(screen+`}`))
	if state := s.Snapshot(); !state.ScreenKnown || state.CardTransparencyKnown {
		t.Fatal("legacy screen state should not enable card controls")
	}
	if err := s.SetCardTransparency(60); err == nil || len(conn.messages) != 0 {
		t.Fatalf("sent an unsupported command: %v", err)
	}
	s.handleDeviceFrame(conn, []byte(screen+`,"card_transparency":60}`))
	for _, invalid := range []int{-1, 101} {
		if err := s.SetCardTransparency(invalid); err == nil || len(conn.messages) != 0 {
			t.Fatalf("sent an invalid command: %d, %v", invalid, err)
		}
	}
	if err := s.SetCardTransparency(0); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages) != 1 || string(conn.messages[0]) != `{"v":1,"type":"card_transparency","percent":0}` {
		t.Fatalf("wrong command: %q", conn.messages)
	}
	s.handleDeviceFrame(conn, []byte(screen+`,"card_transparency":101}`))
	s.handleDeviceFrame(&fakeConn{}, []byte(screen+`,"card_transparency":100}`))
	if state := s.Snapshot(); !state.CardTransparencyKnown || state.CardTransparency != 60 {
		t.Fatal("a send, invalid report, or stale connection changed the reported value")
	}
	s.handleDeviceFrame(conn, []byte(screen+`,"card_transparency":0}`))
	if state := s.Snapshot(); !state.CardTransparencyKnown || state.CardTransparency != 0 || state.ScreenOn || state.Brightness != 42 {
		t.Fatal("valid card report should update transparency independently of screen settings")
	}
	if err := s.Stop(); err != nil || s.Snapshot().CardTransparencyKnown {
		t.Fatalf("disconnect should clear device capability: %v", err)
	}
}
