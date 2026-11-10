//go:build (!darwin && !windows) || (darwin && !cgo)

package holdkey

func start(func(bool), bool) (func(), error) { return nil, ErrUnsupported }
