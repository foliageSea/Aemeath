package voice

import (
	"context"
	"testing"
)

func TestDisabledRunEmitsWithoutConnectionOrInitialization(t *testing.T) {
	var states []State
	// Empty factories deliberately panic if disabled runtime touches dependencies.
	err := runWithDependencies(context.Background(), "invalid address", Config{}, nil, nil, func(s State) { states = append(states, s) }, dependencies{})
	if err != nil || len(states) != 1 || states[0].Connected || states[0].WakeReady || states[0].Phase != "disabled" {
		t.Fatalf("disabled run: %+v %v", states, err)
	}
}
