//go:build darwin

package collect

import "testing"

func TestDarwinSensorsDump(t *testing.T) {
	for _, r := range readTemps() {
		t.Logf("%-32s %.1f", r.name, r.val)
	}
	s := newSensors()
	t.Logf("cpu temp: %v", deref(s.CPUTemp()))
	if g := s.GPU(); g != nil {
		t.Logf("gpu: %q load=%v temp=%v", g.Name, deref(g.Load), deref(g.Temp))
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
