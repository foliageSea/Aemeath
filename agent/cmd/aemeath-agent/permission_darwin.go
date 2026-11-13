//go:build darwin && cgo

package main

import "C"

// The accompanying C file embeds microphone permission metadata in the CLI
// executable so both go run and go build work outside a macOS app bundle.
