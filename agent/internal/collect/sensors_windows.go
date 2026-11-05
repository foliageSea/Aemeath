//go:build windows

package collect

import (
	"strings"
	"syscall"
	"unsafe"

	"aemeath/agent/internal/proto"

	"github.com/yusufpapurcu/wmi"
)

// Temperatures and non-NVIDIA GPUs come from LibreHardwareMonitor's WMI provider
// (LHM must be running, ideally as admin). NVIDIA GPUs are read directly via NVML.
const lhmNamespace = `root\LibreHardwareMonitor`

type lhmSensor struct {
	Name       string
	SensorType string
	Identifier string
	Parent     string
	Value      float32
}

type lhmHardware struct {
	Name         string
	Identifier   string
	HardwareType string
}

type winSensors struct {
	nvml *nvml
	lhm  []lhmSensor // refreshed once per CPUTemp call, reused by GPU
}

func newSensors() sensors {
	return &winSensors{nvml: loadNVML()}
}

func (s *winSensors) Close() {
	if s.nvml != nil {
		s.nvml.shutdown()
	}
}

func (s *winSensors) refreshLHM() {
	s.lhm = nil
	var rows []lhmSensor
	q := "SELECT Name, SensorType, Identifier, Parent, Value FROM Sensor WHERE SensorType='Temperature' OR SensorType='Load' OR SensorType='SmallData'"
	if err := wmi.QueryNamespace(q, &rows, lhmNamespace); err == nil {
		s.lhm = rows
	}
}

func (s *winSensors) CPUTemp() *float64 {
	s.refreshLHM()
	var pkg, hottest *float64
	for _, r := range s.lhm {
		if r.SensorType != "Temperature" || !strings.Contains(r.Identifier, "cpu") {
			continue
		}
		v := float64(r.Value)
		if v <= 0 || v > 150 {
			continue
		}
		if strings.Contains(r.Name, "Package") || strings.Contains(r.Name, "Tctl") || strings.Contains(r.Name, "Tdie") {
			pkg = proto.F(v)
		}
		if hottest == nil || v > *hottest {
			hottest = proto.F(v)
		}
	}
	if pkg != nil {
		return pkg
	}
	if hottest != nil {
		return hottest
	}
	return acpiTemp()
}

// acpiTemp is a coarse fallback; needs admin and many boards report a fixed value.
func acpiTemp() *float64 {
	var rows []struct{ CurrentTemperature uint32 }
	if err := wmi.QueryNamespace("SELECT CurrentTemperature FROM MSAcpi_ThermalZoneTemperature", &rows, `root\WMI`); err != nil || len(rows) == 0 {
		return nil
	}
	c := float64(rows[0].CurrentTemperature)/10 - 273.15
	if c <= 0 || c > 150 {
		return nil
	}
	return proto.F(c)
}

func (s *winSensors) GPU() *proto.GPU {
	if s.nvml != nil {
		if g := s.nvml.read(); g != nil {
			return g
		}
	}
	return s.lhmGPU()
}

func (s *winSensors) lhmGPU() *proto.GPU {
	var hw []lhmHardware
	if err := wmi.QueryNamespace("SELECT Name, Identifier, HardwareType FROM Hardware", &hw, lhmNamespace); err != nil {
		return nil
	}
	// Prefer a discrete GPU over integrated graphics.
	var gpu *lhmHardware
	for i := range hw {
		h := &hw[i]
		if !strings.HasPrefix(h.HardwareType, "Gpu") {
			continue
		}
		if gpu == nil || !strings.Contains(h.Identifier, "intel") {
			gpu = h
		}
	}
	if gpu == nil {
		return nil
	}
	g := &proto.GPU{Name: gpu.Name}
	for _, r := range s.lhm {
		if r.Parent != gpu.Identifier {
			continue
		}
		v := float64(r.Value)
		switch {
		case r.SensorType == "Load" && r.Name == "GPU Core":
			g.Load = proto.F(v)
		case r.SensorType == "Temperature" && (r.Name == "GPU Core" || (g.Temp == nil && strings.HasPrefix(r.Name, "GPU"))):
			g.Temp = proto.F(v)
		case r.SensorType == "SmallData" && r.Name == "GPU Memory Used":
			g.MemUsed = proto.U(uint64(v * 1024 * 1024))
		case r.SensorType == "SmallData" && r.Name == "GPU Memory Total":
			g.MemTotal = proto.U(uint64(v * 1024 * 1024))
		}
	}
	return g
}

// nvml is a minimal binding to nvml.dll (installed with the NVIDIA driver).
type nvml struct {
	dll                                                 *syscall.LazyDLL
	shutdownFn, handleFn, utilFn, tempFn, memFn, nameFn *syscall.LazyProc
	dev                                                 uintptr
	name                                                string
}

func loadNVML() *nvml {
	dll := syscall.NewLazyDLL("nvml.dll")
	if dll.Load() != nil {
		return nil
	}
	n := &nvml{
		dll:        dll,
		shutdownFn: dll.NewProc("nvmlShutdown"),
		handleFn:   dll.NewProc("nvmlDeviceGetHandleByIndex_v2"),
		utilFn:     dll.NewProc("nvmlDeviceGetUtilizationRates"),
		tempFn:     dll.NewProc("nvmlDeviceGetTemperature"),
		memFn:      dll.NewProc("nvmlDeviceGetMemoryInfo"),
		nameFn:     dll.NewProc("nvmlDeviceGetName"),
	}
	if r, _, _ := dll.NewProc("nvmlInit_v2").Call(); r != 0 {
		return nil
	}
	if r, _, _ := n.handleFn.Call(0, uintptr(unsafe.Pointer(&n.dev))); r != 0 {
		n.shutdownFn.Call()
		return nil
	}
	buf := make([]byte, 96)
	if r, _, _ := n.nameFn.Call(n.dev, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); r == 0 {
		n.name = strings.TrimRight(string(buf), "\x00")
	}
	return n
}

func (n *nvml) shutdown() { n.shutdownFn.Call() }

func (n *nvml) read() *proto.GPU {
	var util struct{ GPU, Memory uint32 }
	if r, _, _ := n.utilFn.Call(n.dev, uintptr(unsafe.Pointer(&util))); r != 0 {
		return nil
	}
	g := &proto.GPU{Name: n.name, Load: proto.F(float64(util.GPU))}
	var temp uint32
	if r, _, _ := n.tempFn.Call(n.dev, 0, uintptr(unsafe.Pointer(&temp))); r == 0 {
		g.Temp = proto.F(float64(temp))
	}
	var mem struct{ Total, Free, Used uint64 }
	if r, _, _ := n.memFn.Call(n.dev, uintptr(unsafe.Pointer(&mem))); r == 0 {
		g.MemUsed, g.MemTotal = proto.U(mem.Used), proto.U(mem.Total)
	}
	return g
}
