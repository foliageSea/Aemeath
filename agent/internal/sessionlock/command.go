package sessionlock

import (
	"context"
	"os"
	"os/exec"
)

// Static system path and argv only. Output is discarded: errors must not leak
// session names, process environment, or text supplied by an external program.
const cgSessionPath = "/System/Library/CoreServices/Menu Extras/User.menu/Contents/Resources/CGSession"

type commandRunner func(context.Context, string, ...string) error

func runSystemCommand(ctx context.Context, name string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = requestTimeout
	return cmd.Run()
}
func lockCGSession(ctx context.Context, available func(string) bool, run commandRunner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !available(cgSessionPath) {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := run(ctx, cgSessionPath, "-suspend"); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrRejected
	}
	return nil
}
func systemExecutable(name string) bool {
	info, err := os.Stat(name)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}
