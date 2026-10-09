package profile

import (
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// ApplyCgroup narrows a Linux profile to the cgroup (v2) this process runs in:
// a container capped at 6 GB on a 32 GB host has 6 GB to give a model, and
// /proc/meminfo and the CPU count say the host's. The tightest memory.max and
// cpu.max from this process's group up to the root bind; RAMAvail becomes the
// cap less what that group already uses when that is smaller. procSelfCgroup
// is the text of /proc/self/cgroup; sysfs is where the v2 hierarchy is mounted.
func ApplyCgroup(p *Profile, sysfs, procSelfCgroup string) {
	rel := ""
	for _, line := range strings.Split(procSelfCgroup, "\n") {
		if strings.HasPrefix(line, "0::") {
			rel = strings.TrimSpace(strings.TrimPrefix(line, "0::"))
		}
	}
	if rel == "" {
		return
	}
	read := func(dir, name string) string {
		b, err := os.ReadFile(filepath.Join(sysfs, filepath.FromSlash(dir), name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	var memCap, memUsed uint64
	cpus := 0
	for dir := path.Clean("/" + rel); ; dir = path.Dir(dir) {
		if n, err := strconv.ParseUint(read(dir, "memory.max"), 10, 64); err == nil && (memCap == 0 || n < memCap) {
			memCap = n
			memUsed, _ = strconv.ParseUint(read(dir, "memory.current"), 10, 64)
		}
		if f := strings.Fields(read(dir, "cpu.max")); len(f) == 2 && f[0] != "max" {
			quota, e1 := strconv.ParseFloat(f[0], 64)
			period, e2 := strconv.ParseFloat(f[1], 64)
			if e1 == nil && e2 == nil && period > 0 {
				n := int((quota + period - 1) / period)
				if n < 1 {
					n = 1
				}
				if cpus == 0 || n < cpus {
					cpus = n
				}
			}
		}
		if dir == "/" {
			break
		}
	}
	if memCap > 0 && (p.RAM == 0 || memCap < p.RAM) {
		p.RAM = memCap
		if free := memCap - min(memUsed, memCap); p.RAMAvail == 0 || free < p.RAMAvail {
			p.RAMAvail = free
		}
	}
	if cpus > 0 && cpus < p.CPUs {
		p.CPUs = cpus
	}
}
