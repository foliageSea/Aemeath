//go:build darwin

package collect

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <stdint.h>
#include <string.h>
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>

// Private IOHID API used for Apple Silicon temperature sensors (no root needed).
typedef struct __IOHIDEventSystemClient *IOHIDEventSystemClientRef;
typedef struct __IOHIDServiceClient *IOHIDServiceClientRef;
typedef struct __IOHIDEvent *IOHIDEventRef;
extern IOHIDEventSystemClientRef IOHIDEventSystemClientCreate(CFAllocatorRef allocator);
extern int IOHIDEventSystemClientSetMatching(IOHIDEventSystemClientRef client, CFDictionaryRef match);
extern CFArrayRef IOHIDEventSystemClientCopyServices(IOHIDEventSystemClientRef client);
extern IOHIDEventRef IOHIDServiceClientCopyEvent(IOHIDServiceClientRef service, int64_t type, int32_t options, int64_t timestamp);
extern CFTypeRef IOHIDServiceClientCopyProperty(IOHIDServiceClientRef service, CFStringRef key);
extern double IOHIDEventGetFloatValue(IOHIDEventRef event, int32_t field);

#define HID_EVENT_TEMPERATURE 15

static IOHIDEventSystemClientRef temp_client(void) {
	static IOHIDEventSystemClientRef client;
	if (client) return client;
	client = IOHIDEventSystemClientCreate(kCFAllocatorDefault);
	if (!client) return NULL;
	int page = 0xff00, usage = 5;
	CFNumberRef p = CFNumberCreate(NULL, kCFNumberIntType, &page);
	CFNumberRef u = CFNumberCreate(NULL, kCFNumberIntType, &usage);
	const void *keys[] = {CFSTR("PrimaryUsagePage"), CFSTR("PrimaryUsage")};
	const void *vals[] = {p, u};
	CFDictionaryRef match = CFDictionaryCreate(NULL, keys, vals, 2,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	IOHIDEventSystemClientSetMatching(client, match);
	CFRelease(match); CFRelease(p); CFRelease(u);
	return client;
}

// Fills names (64 bytes each) and values; returns the count.
static int read_temps(char *names, double *vals, int max) {
	IOHIDEventSystemClientRef client = temp_client();
	if (!client) return 0;
	CFArrayRef services = IOHIDEventSystemClientCopyServices(client);
	if (!services) return 0;
	int n = 0;
	CFIndex count = CFArrayGetCount(services);
	for (CFIndex i = 0; i < count && n < max; i++) {
		IOHIDServiceClientRef sc = (IOHIDServiceClientRef)CFArrayGetValueAtIndex(services, i);
		CFTypeRef name = IOHIDServiceClientCopyProperty(sc, CFSTR("Product"));
		IOHIDEventRef ev = IOHIDServiceClientCopyEvent(sc, HID_EVENT_TEMPERATURE, 0, 0);
		if (name && CFGetTypeID(name) == CFStringGetTypeID() && ev) {
			CFStringGetCString((CFStringRef)name, names + n * 64, 64, kCFStringEncodingUTF8);
			vals[n] = IOHIDEventGetFloatValue(ev, HID_EVENT_TEMPERATURE << 16);
			n++;
		}
		if (ev) CFRelease(ev);
		if (name) CFRelease(name);
	}
	CFRelease(services);
	return n;
}

static int64_t dict_int(CFDictionaryRef d, CFStringRef key) {
	CFNumberRef v = CFDictionaryGetValue(d, key);
	int64_t out = -1;
	if (v && CFGetTypeID(v) == CFNumberGetTypeID()) CFNumberGetValue(v, kCFNumberSInt64Type, &out);
	return out;
}

// GPU utilisation from the first IOAccelerator that reports it.
static int read_gpu(int64_t *util, int64_t *mem_used, char *model, int model_len) {
	io_iterator_t it;
	if (IOServiceGetMatchingServices(kIOMainPortDefault, IOServiceMatching("IOAccelerator"), &it) != KERN_SUCCESS)
		return 0;
	int found = 0;
	io_registry_entry_t e;
	while (!found && (e = IOIteratorNext(it))) {
		CFTypeRef stats = IORegistryEntryCreateCFProperty(e, CFSTR("PerformanceStatistics"), NULL, 0);
		if (stats && CFGetTypeID(stats) == CFDictionaryGetTypeID()) {
			*util = dict_int(stats, CFSTR("Device Utilization %"));
			*mem_used = dict_int(stats, CFSTR("In use system memory"));
			found = *util >= 0;
			model[0] = 0;
			CFTypeRef m = IORegistryEntrySearchCFProperty(e, kIOServicePlane, CFSTR("model"), NULL,
				kIORegistryIterateRecursively | kIORegistryIterateParents);
			if (m) {
				if (CFGetTypeID(m) == CFStringGetTypeID()) {
					CFStringGetCString(m, model, model_len, kCFStringEncodingUTF8);
				} else if (CFGetTypeID(m) == CFDataGetTypeID()) {
					CFIndex l = CFDataGetLength(m);
					if (l >= model_len) l = model_len - 1;
					memcpy(model, CFDataGetBytePtr(m), l);
					model[l] = 0;
				}
				CFRelease(m);
			}
		}
		if (stats) CFRelease(stats);
		IOObjectRelease(e);
	}
	IOObjectRelease(it);
	return found;
}
*/
import "C"

import (
	"strings"
	"unsafe"

	"aemeath/agent/internal/proto"

	"github.com/shirou/gopsutil/v4/mem"
)

const maxSensors = 128

type darwinSensors struct {
	gpuName  string
	memTotal uint64
}

func newSensors() sensors {
	s := &darwinSensors{}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.memTotal = vm.Total // unified memory
	}
	return s
}

func (s *darwinSensors) Close() {}

type reading struct {
	name string
	val  float64
}

func readTemps() []reading {
	names := make([]byte, maxSensors*64)
	vals := make([]C.double, maxSensors)
	n := int(C.read_temps((*C.char)(unsafe.Pointer(&names[0])), &vals[0], maxSensors))
	out := make([]reading, 0, n)
	for i := 0; i < n; i++ {
		raw := names[i*64 : (i+1)*64]
		if z := strings.IndexByte(string(raw), 0); z >= 0 {
			raw = raw[:z]
		}
		v := float64(vals[i])
		if v > 0 && v < 150 {
			out = append(out, reading{string(raw), v})
		}
	}
	return out
}

// maxOf returns the hottest sensor whose name contains any of the keys.
func maxOf(rs []reading, keys ...string) *float64 {
	var best *float64
	for _, r := range rs {
		for _, k := range keys {
			if strings.Contains(r.name, k) {
				if best == nil || r.val > *best {
					best = proto.F(r.val)
				}
				break
			}
		}
	}
	return best
}

func (s *darwinSensors) CPUTemp() *float64 {
	rs := readTemps()
	if t := maxOf(rs, "pACC", "eACC", "CPU"); t != nil {
		return t
	}
	return maxOf(rs, "tdie")
}

func (s *darwinSensors) GPU() *proto.GPU {
	var util, used C.int64_t
	model := make([]byte, 128)
	if C.read_gpu(&util, &used, (*C.char)(unsafe.Pointer(&model[0])), C.int(len(model))) == 0 {
		return nil
	}
	name := strings.TrimRight(string(model), "\x00")
	if name != "" && !strings.Contains(name, "GPU") {
		name += " GPU"
	}
	g := &proto.GPU{Name: name, Load: proto.F(float64(util))}
	if used >= 0 {
		g.MemUsed = proto.U(uint64(used))
		if s.memTotal > 0 {
			g.MemTotal = proto.U(s.memTotal)
		}
	}
	g.Temp = maxOf(readTemps(), "GPU")
	return g
}
