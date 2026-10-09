package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseNvidiaSmi(t *testing.T) {
	out := "NVIDIA GeForce RTX 5080, 16303, 580.95.05, 12.0\nNVIDIA GeForce GTX 1650 Ti, 4096, 551.23, 7.5\n\nbroken line\n"
	g := ParseNvidiaSmi(out)
	if len(g) != 2 {
		t.Fatalf("%+v", g)
	}
	if g[0].Name != "NVIDIA GeForce RTX 5080" || g[0].VRAM != 16303<<20 || g[0].Driver != "580.95.05" || g[0].ComputeCap != "12.0" || g[0].Vendor != "nvidia" {
		t.Fatalf("%+v", g[0])
	}
	old := ParseNvidiaSmi("Tesla K80, 11441, 470.82")
	if len(old) != 1 || old[0].ComputeCap != "" || old[0].VRAM != 11441<<20 {
		t.Fatalf("%+v", old)
	}
	if got := ParseNvidiaSmi("NVIDIA T4, [N/A], 1, 1"); len(got) != 0 {
		t.Fatal("unreadable memory accepted")
	}
}

func TestParseMeminfo(t *testing.T) {
	const m = "MemTotal:       32616164 kB\nMemFree:         1000000 kB\nMemAvailable:   20000000 kB\nBuffers: 5 kB\n"
	total, avail := ParseMeminfo(m)
	if total != 32616164*1024 || avail != 20000000*1024 {
		t.Fatalf("%d %d", total, avail)
	}
	_, avail = ParseMeminfo("MemTotal: 100 kB\nMemFree: 10 kB\nBuffers: 20 kB\nCached: 30 kB\n")
	if avail != 60*1024 {
		t.Fatalf("fallback %d", avail)
	}
}

func TestParseVMStat(t *testing.T) {
	const v = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10000.
Pages active:                            500000.
Pages inactive:                           20000.
Pages speculative:                         5000.
Pages wired down:                        100000.
`
	if got := ParseVMStat(v); got != 35000*16384 {
		t.Fatalf("%d", got)
	}
	if ParseSysctlValue("17179869184\n") != 17179869184 {
		t.Fatal("sysctl")
	}
}

func TestParseWindows(t *testing.T) {
	total, avail, err := ParseWindowsMemory(`{"TotalVisibleMemorySize": 16680000, "FreePhysicalMemory": 4000000}`)
	if err != nil || total != 16680000*1024 || avail != 4000000*1024 {
		t.Fatalf("%d %d %v", total, avail, err)
	}
	// Several adapters: an array; one with no size (a basic display) is skipped.
	const dump = `[
 {"DriverDesc":"AMD Radeon RX 7800 XT","ProviderName":"Advanced Micro Devices, Inc.","Mem":17163091968},
 {"DriverDesc":"Microsoft Basic Display Adapter","ProviderName":"Microsoft","Mem":null},
 {"DriverDesc":"Intel(R) UHD Graphics","ProviderName":"Intel Corporation","Mem":[0,0,0,64,0,0,0,0]}
]`
	g, err := ParseWindowsDisplayClass(dump)
	if err != nil || len(g) != 2 {
		t.Fatalf("%+v %v", g, err)
	}
	if g[0].Vendor != "amd" || g[0].VRAM != 17163091968 || g[1].Vendor != "intel" || g[1].VRAM != 1<<30 {
		t.Fatalf("%+v", g)
	}
	one, err := ParseWindowsDisplayClass(`{"DriverDesc":"NVIDIA RTX","ProviderName":"NVIDIA","Mem":8589934592}`)
	if err != nil || len(one) != 1 || one[0].Vendor != "nvidia" {
		t.Fatalf("%+v %v", one, err)
	}
	if none, _ := ParseWindowsDisplayClass(""); none != nil {
		t.Fatal("empty dump")
	}
}

func TestReadDRM(t *testing.T) {
	root := t.TempDir()
	mk := func(card, vendor, mem, name string) {
		d := filepath.Join(root, card, "device")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "vendor"), []byte(vendor+"\n"), 0o644)
		if mem != "" {
			os.WriteFile(filepath.Join(d, "mem_info_vram_total"), []byte(mem+"\n"), 0o644)
		}
		if name != "" {
			os.WriteFile(filepath.Join(d, "product_name"), []byte(name+"\n"), 0o644)
		}
	}
	mk("card0", "0x1002", "17163091968", "Radeon RX 7800 XT")
	mk("card1", "0x8086", "", "")           // integrated: no dedicated memory
	mk("card2", "0x10de", "8589934592", "") // NVIDIA is left to nvidia-smi
	mk("card3", "0x8086", "8589934592", "")
	g := ReadDRM(root)
	if len(g) != 2 || g[0].Vendor != "amd" || g[0].VRAM != 17163091968 || g[0].Name != "Radeon RX 7800 XT" || g[1].Vendor != "intel" {
		t.Fatalf("%+v", g)
	}
}

func TestMetalBudget(t *testing.T) {
	if got := (Profile{RAM: 16 << 30}).MetalBudget(); got != 12<<30 {
		t.Fatalf("16 GiB: %d", got)
	}
	if got := (Profile{RAM: 64 << 30}).MetalBudget(); got != 54975581388 {
		t.Fatalf("64 GiB: %d", got)
	}
}

func TestInstallRoots(t *testing.T) {
	none := func(string) string { return "" }
	if got := installRoot("linux", none, "/h"); got != filepath.Join("/h", ".local", "share", "wasp") {
		t.Fatal(got)
	}
	if got := installRoot("darwin", none, "/h"); got != filepath.Join("/h", "Library", "Application Support", "wasp") {
		t.Fatal(got)
	}
	env := func(k string) string {
		if k == "LOCALAPPDATA" {
			return `D:\Local`
		}
		return ""
	}
	if got := installRoot("windows", env, "/h"); got != filepath.Join(`D:\Local`, "wasp") {
		t.Fatal(got)
	}
}

func TestWSLVersion(t *testing.T) {
	if !isWSLVersion("Linux version 5.15.90.1-microsoft-standard-WSL2") || isWSLVersion("Linux version 6.1.0 (gcc)") {
		t.Fatal("wsl")
	}
}

func TestOverrideFilePosesAsAnyMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	os.WriteFile(path, []byte(`{"os":"darwin","arch":"arm64","cpus":10,"ram":17179869184,"ram_avail":8000000000,"unified":true,"gpus":[{"vendor":"apple","name":"M2","vram":12884901888}]}`), 0o644)
	t.Setenv("WASP_PROFILE_JSON", path)
	p := Detect()
	if p.OS != "darwin" || !p.Unified || p.GPUs[0].VRAM != 12<<30 {
		t.Fatalf("%+v", p)
	}
	t.Setenv("WASP_PROFILE_JSON", "")
	t.Setenv("HIVE_PROFILE_JSON", path)
	if Detect().OS != "darwin" {
		t.Fatal("HIVE_PROFILE_JSON ignored")
	}
}

func TestDetectFindsNvidiaSmiInADriverDir(t *testing.T) {
	dir := t.TempDir()
	smi := filepath.Join(dir, "nvidia-smi")
	if err := os.WriteFile(smi, []byte("#!/bin/sh\necho 'Fake GPU, 8192, 550.1, 8.9'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := NvidiaDirs
	NvidiaDirs = []string{dir}
	defer func() { NvidiaDirs = old }()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WASP_NVIDIA_SMI", "")
	t.Setenv("HIVE_NVIDIA_SMI", "")
	if got := NvidiaSmiPath(); got != smi && filepath.Base(got) != "nvidia-smi" {
		t.Fatalf("path %q", got)
	}
}

func TestDetectHere(t *testing.T) {
	t.Setenv("WASP_PROFILE_JSON", "")
	t.Setenv("HIVE_PROFILE_JSON", "")
	p := Detect()
	if p.OS == "" || p.CPUs < 1 || p.RAM == 0 {
		t.Fatalf("%+v", p)
	}
}
