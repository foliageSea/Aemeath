//go:build !darwin && !windows

package collect

import "aemeath/agent/internal/proto"

type noSensors struct{}

func newSensors() sensors           { return noSensors{} }
func (noSensors) CPUTemp() *float64 { return nil }
func (noSensors) GPU() *proto.GPU   { return nil }
func (noSensors) Close()            {}
