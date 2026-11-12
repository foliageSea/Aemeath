//go:build !cgo || (!darwin && !windows && !linux)

package audiooutput

import "errors"

func startDevice(_ *Player, _ int) (func(), error) {
	return nil, errors.New("console audio output requires a cgo build on macOS, Windows or Linux")
}
