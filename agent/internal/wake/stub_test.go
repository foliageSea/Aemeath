//go:build !cgo

package wake

import (
	"errors"
	"testing"
)

func TestNoCGOUnavailable(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := NewVAD(""); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
