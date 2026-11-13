//go:build (!darwin && !windows) || (darwin && !cgo)

package holdkey

func start(func(bool), shortcut) (func(), error) { return nil, ErrUnsupported }
