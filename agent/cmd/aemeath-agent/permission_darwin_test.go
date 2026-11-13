//go:build darwin && cgo

package main

import (
	"bytes"
	"debug/macho"
	"os"
	"testing"
)

func TestCLIMacOSMicrophonePermissionMetadata(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := macho.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	section := binary.Section("__info_plist")
	if section == nil || section.Seg != "__TEXT" {
		t.Fatal("CLI executable lacks __TEXT,__info_plist permission metadata")
	}
	data, err := section.Data()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("<key>NSMicrophoneUsageDescription</key><string>Aemeath uses the system microphone")) {
		t.Fatal("CLI executable lacks microphone usage description")
	}
}
