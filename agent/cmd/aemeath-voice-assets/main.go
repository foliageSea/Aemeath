// aemeath-voice-assets explicitly installs pinned offline voice assets.
package main

import (
	"aemeath/agent/internal/wake"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	dir := flag.String("dir", wake.DefaultAssetDir(), "voice asset cache directory")
	reference := flag.Bool("reference-only", false, "install or verify only the reference WAV")
	verify := flag.Bool("verify", false, "verify local assets without downloading")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments; use -help")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var err error
	if *verify {
		err = wake.VerifyAssets(*dir, *reference)
	} else {
		err = wake.InstallAssets(ctx, *dir, *reference)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "voice assets:", err)
		os.Exit(1)
	}
	fmt.Printf("Voice assets verified: %s\nChinese wake: %s; sherpa-onnx %s; Silero VAD\n", *dir, wake.Keyword, wake.BindingVersion)
}
