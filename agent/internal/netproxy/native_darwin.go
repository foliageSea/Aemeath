//go:build darwin

package netproxy

import (
	"context"
	"io"
	"os/exec"
)

// Do not embed bytes.Buffer: its promoted ReadFrom would bypass Write limits
// when os/exec copies stdout with io.Copy.
type limitedOutput struct{ data []byte }

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(w.data)+len(p) > 64*1024 {
		return 0, io.ErrShortWrite
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *limitedOutput) String() string { return string(w.data) }

func readNative(ctx context.Context) (nativeConfig, error) {
	cmd := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy")
	output := &limitedOutput{}
	cmd.Stdout = output
	// Do not capture stderr: it can contain OS-specific details or credentials.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nativeConfig{}, ctx.Err()
		}
		return nativeConfig{}, errNativeSettings
	}
	return parseSCUtil(output.String())
}
