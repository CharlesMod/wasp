package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const mib = 1 << 20

// ParseNvidiaSmi reads the output of
// nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap --format=csv,noheader,nounits
// (memory in MiB). Lines it cannot read are skipped; an older driver without
// compute_cap prints three fields, which parse as well.
func ParseNvidiaSmi(out string) []GPU {
	var gpus []GPU
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, ",")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		if len(f) < 2 {
			continue
		}
		mem, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		g := GPU{Vendor: "nvidia", Name: f[0], VRAM: mem * mib}
		if len(f) > 2 {
			g.Driver = f[2]
		}
		if len(f) > 3 && f[3] != "[N/A]" {
			g.ComputeCap = f[3]
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// ParseMeminfo reads /proc/meminfo: MemTotal and MemAvailable (falling back to
// MemFree+Buffers+Cached on kernels without it), in bytes.
func ParseMeminfo(s string) (total, avail uint64) {
	vals := map[string]uint64{}
	for _, line := range strings.Split(s, "\n") {
		k, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fs := strings.Fields(rest)
		if len(fs) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fs[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fs) > 1 && strings.EqualFold(fs[1], "kB") {
			n *= 1024
		}
		vals[k] = n
	}
	total = vals["MemTotal"]
	if a, ok := vals["MemAvailable"]; ok {
		return total, a
	}
	return total, vals["MemFree"] + vals["Buffers"] + vals["Cached"]
}

var pageSizeRE = regexp.MustCompile(`page size of (\d+) bytes`)
var vmLineRE = regexp.MustCompile(`^Pages ([a-z ]+):\s+(\d+)\.`)

// ParseVMStat reads macOS vm_stat and returns the bytes that can be handed to
// a program without swapping: free, inactive and speculative pages.
func ParseVMStat(s string) uint64 {
	page := uint64(4096)
	if m := pageSizeRE.FindStringSubmatch(s); m != nil {
		page, _ = strconv.ParseUint(m[1], 10, 64)
	}
	var pages uint64
	for _, line := range strings.Split(s, "\n") {
		m := vmLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		switch m[1] {
		case "free", "inactive", "speculative":
			n, _ := strconv.ParseUint(m[2], 10, 64)
			pages += n
		}
	}
	return pages * page
}

// ParseSysctlValue reads `sysctl -n` output as an unsigned integer.
func ParseSysctlValue(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

// ParseWindowsMemory reads the JSON of
// Get-CimInstance Win32_OperatingSystem | Select TotalVisibleMemorySize,FreePhysicalMemory | ConvertTo-Json
// (both in KiB).
func ParseWindowsMemory(s string) (total, avail uint64, err error) {
	var v struct {
		Total uint64 `json:"TotalVisibleMemorySize"`
		Free  uint64 `json:"FreePhysicalMemory"`
	}
	if err = json.Unmarshal([]byte(s), &v); err != nil {
		return 0, 0, err
	}
	return v.Total * 1024, v.Free * 1024, nil
}

// ParseWindowsDisplayClass reads the JSON of a PowerShell dump of the display
// class registry keys (…\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}\0*),
// projected to DriverDesc, ProviderName and Mem (HardwareInformation.qwMemorySize).
// The registry value is a 64-bit count, which Win32_VideoController.AdapterRAM
// is not (it caps at 4 GB). Entries without a size are skipped. The JSON is an
// object when one adapter answers and an array when several do.
func ParseWindowsDisplayClass(s string) ([]GPU, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	type entry struct {
		Desc     string          `json:"DriverDesc"`
		Provider string          `json:"ProviderName"`
		Mem      json.RawMessage `json:"Mem"`
	}
	var list []entry
	if strings.HasPrefix(s, "[") {
		if err := json.Unmarshal([]byte(s), &list); err != nil {
			return nil, err
		}
	} else {
		var e entry
		if err := json.Unmarshal([]byte(s), &e); err != nil {
			return nil, err
		}
		list = []entry{e}
	}
	var gpus []GPU
	for _, e := range list {
		mem := rawUint(e.Mem)
		if mem == 0 {
			continue
		}
		gpus = append(gpus, GPU{Vendor: vendorOf(e.Provider + " " + e.Desc), Name: e.Desc, VRAM: mem})
	}
	return gpus, nil
}

// rawUint reads a JSON number, or a JSON array of bytes (a REG_BINARY the
// shell printed as bytes, little endian), as an unsigned integer.
func rawUint(r json.RawMessage) uint64 {
	if len(r) == 0 {
		return 0
	}
	var n uint64
	if json.Unmarshal(r, &n) == nil {
		return n
	}
	var f float64
	if json.Unmarshal(r, &f) == nil && f > 0 {
		return uint64(f)
	}
	var bs []uint8
	if json.Unmarshal(r, &bs) == nil {
		for i := len(bs) - 1; i >= 0; i-- {
			n = n<<8 | uint64(bs[i])
		}
		return n
	}
	return 0
}

func vendorOf(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "nvidia"):
		return "nvidia"
	case strings.Contains(s, "amd") || strings.Contains(s, "advanced micro") || strings.Contains(s, "radeon"):
		return "amd"
	case strings.Contains(s, "intel"):
		return "intel"
	case strings.Contains(s, "apple"):
		return "apple"
	}
	return "other"
}

// ReadDRM reads the AMD and Intel cards that report dedicated memory under
// sysRoot (/sys/class/drm): card*/device/mem_info_vram_total, with the PCI
// vendor id picking the vendor. NVIDIA cards are left to nvidia-smi.
func ReadDRM(sysRoot string) []GPU {
	cards, _ := filepath.Glob(filepath.Join(sysRoot, "card*", "device", "mem_info_vram_total"))
	var gpus []GPU
	for _, f := range cards {
		dev := filepath.Dir(f)
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		mem, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || mem == 0 {
			continue
		}
		vb, _ := os.ReadFile(filepath.Join(dev, "vendor"))
		vendor := map[string]string{"0x1002": "amd", "0x8086": "intel", "0x10de": "nvidia"}[strings.TrimSpace(string(vb))]
		if vendor == "" {
			vendor = "other"
		}
		if vendor == "nvidia" {
			continue
		}
		name := ""
		if nb, err := os.ReadFile(filepath.Join(dev, "product_name")); err == nil {
			name = strings.TrimSpace(string(nb))
		}
		if name == "" {
			name = fmt.Sprintf("%s GPU (%s)", vendor, filepath.Base(filepath.Dir(dev)))
		}
		gpus = append(gpus, GPU{Vendor: vendor, Name: name, VRAM: mem})
	}
	return gpus
}
