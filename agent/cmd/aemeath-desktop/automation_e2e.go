//go:build e2e

package main

// Enable the local automation socket only in explicit integration-test builds.
import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/plugins/automation"
)

func init() {
	mygo.App.OnWindowCreated(func(w *mygo.Window) {
		if os.Getenv("AEMEATH_TEST_COMPACT") == "1" {
			w.SetSize(760, 700)
		}
	})
	if dir := os.Getenv("AEMEATH_TEST_DATA_DIR"); dir != "" {
		mygo.App.SetPath(mygo.PathUserData, dir)
		mygo.App.SetPath(mygo.PathLogs, filepath.Join(dir, "logs"))
	}
}
