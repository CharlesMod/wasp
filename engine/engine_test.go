package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CharlesMod/wasp/fit"
	"github.com/CharlesMod/wasp/profile"
)

func tarGz(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = "x" + h.Name
			h.Size = int64(len(body))
		}
		if h.Mode == 0 {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, _ := zw.Create(n)
		w.Write([]byte("z" + n))
	}
	zw.Close()
	return buf.Bytes()
}

func TestDetectFlavor(t *testing.T) {
	t.Setenv(FlavorEnv, "")
	nv := []profile.GPU{{Vendor: "nvidia", VRAM: 8 << 30}}
	amd := []profile.GPU{{Vendor: "amd", VRAM: 8 << 30}}
	cases := []struct {
		name   string
		p      profile.Profile
		vulkan bool
		want   Flavor
	}{
		{"mac", profile.Profile{OS: "darwin", Unified: true}, false, Metal},
		{"windows nvidia", profile.Profile{OS: "windows", GPUs: nv}, false, CUDA},
		{"windows amd", profile.Profile{OS: "windows", GPUs: amd}, false, Vulkan},
		{"linux nvidia", profile.Profile{OS: "linux", GPUs: nv}, false, Vulkan},
		{"linux amd", profile.Profile{OS: "linux", GPUs: amd}, false, Vulkan},
		{"linux no gpu, loader present", profile.Profile{OS: "linux"}, true, Vulkan},
		{"linux no gpu, no loader", profile.Profile{OS: "linux"}, false, CPU},
		{"windows no gpu", profile.Profile{OS: "windows"}, true, CPU},
	}
	for _, c := range cases {
		old := vulkanPresent
		vulkanPresent = func() bool { return c.vulkan }
		if got := DetectFlavor(c.p); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		vulkanPresent = old
	}
	t.Setenv(FlavorEnv, "cpu")
	if DetectFlavor(profile.Profile{OS: "darwin"}) != CPU {
		t.Error("override ignored")
	}
}

func TestAssetsAtThePinnedTag(t *testing.T) {
	if LlamaTag != "b11279" {
		t.Fatalf("pin %s", LlamaTag)
	}
	cases := []struct {
		goos, arch string
		f          Flavor
		want       []string
	}{
		{"linux", "amd64", CPU, []string{"llama-b11279-bin-ubuntu-x64.tar.gz"}},
		{"linux", "amd64", Vulkan, []string{"llama-b11279-bin-ubuntu-vulkan-x64.tar.gz"}},
		{"darwin", "arm64", Metal, []string{"llama-b11279-bin-macos-arm64.tar.gz"}},
		{"windows", "amd64", CPU, []string{"llama-b11279-bin-win-cpu-x64.zip"}},
		{"windows", "amd64", Vulkan, []string{"llama-b11279-bin-win-vulkan-x64.zip"}},
		{"windows", "amd64", CUDA, []string{"llama-b11279-bin-win-cuda-13.3-x64.zip", "cudart-llama-bin-win-cuda-13.3-x64.zip"}},
	}
	for _, c := range cases {
		got, err := Assets(LlamaTag, c.goos, c.arch, c.f)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s/%s %s: %v %v", c.goos, c.arch, c.f, got, err)
		}
	}
	if _, err := Assets(LlamaTag, "linux", "amd64", CUDA); err == nil || !strings.Contains(err.Error(), "no Linux CUDA") {
		t.Errorf("linux cuda: %v", err)
	}
	if _, err := Assets(LlamaTag, "plan9", "amd64", CPU); err == nil {
		t.Error("plan9")
	}
}

// upstream serves archives by asset name and counts requests.
func upstream(files map[string][]byte) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if !strings.HasPrefix(r.URL.Path, "/"+LlamaTag+"/") || files[name] == nil {
			http.NotFound(w, r)
			return
		}
		w.Write(files[name])
	})), &hits
}

func TestInstallLinuxTarballThenReuse(t *testing.T) {
	tgz := tarGz(t,
		tar.Header{Name: "llama-b11279/", Typeflag: tar.TypeDir},
		tar.Header{Name: "llama-b11279/llama-server", Typeflag: tar.TypeReg},
		tar.Header{Name: "llama-b11279/libllama.so.0", Typeflag: tar.TypeReg},
		tar.Header{Name: "llama-b11279/libllama.so", Typeflag: tar.TypeSymlink, Linkname: "libllama.so.0"},
	)
	srv, hits := upstream(map[string][]byte{"llama-b11279-bin-ubuntu-vulkan-x64.tar.gz": tgz})
	defer srv.Close()
	root := t.TempDir()
	rec, err := Install(context.Background(), root, GitHub{BaseURL: srv.URL}, InstallOptions{Flavor: Vulkan, GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "engine", "b11279-vulkan")
	if rec.Tag != LlamaTag || rec.Flavor != "vulkan" || rec.Source != "github" || len(rec.SHA256) != 64 ||
		rec.Server != filepath.Join(dir, "llama-b11279", "llama-server") {
		t.Fatalf("%+v", rec)
	}
	if fi, err := os.Stat(rec.Server); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("server not executable: %v", err)
	}
	if l, err := os.Readlink(filepath.Join(dir, "llama-b11279", "libllama.so")); err != nil || l != "libllama.so.0" {
		t.Fatalf("symlink %q %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "engine.json")); err != nil {
		t.Fatal("no engine.json")
	}
	if _, err := os.Stat(filepath.Join(root, "engine", ".download")); err == nil {
		t.Fatal("download dir left")
	}
	// The second install is the first one again: no request.
	before := hits.Load()
	rec2, err := Install(context.Background(), root, GitHub{BaseURL: srv.URL}, InstallOptions{Flavor: Vulkan, GOOS: "linux", GOARCH: "amd64"})
	if err != nil || rec2.SHA256 != rec.SHA256 || hits.Load() != before {
		t.Fatalf("not reused: %v hits %d->%d", err, before, hits.Load())
	}
	// And Find sees it.
	got, ok := Find(LlamaTag, Vulkan, filepath.Join(root, "engine"))
	if !ok || got.Server != rec.Server {
		t.Fatalf("find %+v", got)
	}
}

func TestInstallWindowsCUDAExtractsBothZips(t *testing.T) {
	srv, _ := upstream(map[string][]byte{
		"llama-b11279-bin-win-cuda-13.3-x64.zip": zipOf(t, "llama-server.exe", "ggml-cuda.dll"),
		"cudart-llama-bin-win-cuda-13.3-x64.zip": zipOf(t, "cudart64_13.dll"),
	})
	defer srv.Close()
	root := t.TempDir()
	rec, err := Install(context.Background(), root, GitHub{BaseURL: srv.URL}, InstallOptions{Flavor: CUDA, GOOS: "windows", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(rec.Server)
	if filepath.Base(rec.Server) != "llama-server.exe" {
		t.Fatal(rec.Server)
	}
	for _, f := range []string{"ggml-cuda.dll", "cudart64_13.dll"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s beside the server: %v", f, err)
		}
	}
}

func TestInstallRefusesAnArchiveWithoutAServer(t *testing.T) {
	srv, _ := upstream(map[string][]byte{"llama-b11279-bin-ubuntu-x64.tar.gz": tarGz(t, tar.Header{Name: "readme", Typeflag: tar.TypeReg})})
	defer srv.Close()
	root := t.TempDir()
	if _, err := Install(context.Background(), root, GitHub{BaseURL: srv.URL}, InstallOptions{GOOS: "linux", GOARCH: "amd64"}); err == nil ||
		!strings.Contains(err.Error(), "no llama-server") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "engine", "b11279-cpu")); err == nil {
		t.Fatal("half install left")
	}
}

func TestExtractRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"slip.tar.gz":    tarGz(t, tar.Header{Name: "../evil", Typeflag: tar.TypeReg}),
		"link.tar.gz":    tarGz(t, tar.Header{Name: "lib", Typeflag: tar.TypeSymlink, Linkname: "../.."}),
		"abslink.tar.gz": tarGz(t, tar.Header{Name: "lib", Typeflag: tar.TypeSymlink, Linkname: "/etc"}),
		"slip.zip":       zipOf(t, "../evil"),
	} {
		p := filepath.Join(t.TempDir(), name)
		os.WriteFile(p, data, 0o644)
		if err := Extract(p, filepath.Join(dir, "out")); err == nil {
			t.Errorf("%s extracted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
		t.Fatal("wrote outside")
	}
}

func TestFindEitherProductsEngine(t *testing.T) {
	waspRoot, hiveRoot := t.TempDir(), t.TempDir()
	mk := func(root, sub, record string) {
		d := filepath.Join(root, sub)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "llama-server"), []byte("#!/bin/sh\n"), 0o755)
		if record != "" {
			os.WriteFile(filepath.Join(d, "engine.json"), []byte(record), 0o644)
		}
	}
	// Wasp's own, a recorded older build.
	mk(waspRoot, "b10621-vulkan", `{"tag":"b10621","flavor":"vulkan","source":"github","sha256":"aa"}`)
	// The Hive's: dirs named tag-flavor with no record, and a record at the root.
	mk(hiveRoot, "b10950-vulkan", "")
	mk(hiveRoot, "b11279-cpu", "")
	os.WriteFile(filepath.Join(hiveRoot, "engine.json"),
		[]byte(`{"version":"b10950","flavor":"vulkan","source":"court","digest":"sha256:bb","server":"`+filepath.Join(hiveRoot, "b10950-vulkan", "llama-server")+`"}`), 0o644)
	os.Symlink(filepath.Join(hiveRoot, "b10950-vulkan"), filepath.Join(hiveRoot, "current"))

	r, ok := Find("b10900", Vulkan, waspRoot, hiveRoot)
	if !ok || r.Tag != "b10950" || r.Source != "court" || r.SHA256 != "bb" {
		t.Fatalf("hive record: %+v %v", r, ok)
	}
	r, ok = Find("b10900", "", waspRoot, hiveRoot)
	if !ok || r.Tag != "b11279" || r.Flavor != "cpu" || r.Source != "found" {
		t.Fatalf("newest: %+v %v", r, ok)
	}
	// "Recent enough": nothing at or above the pin for vulkan.
	if _, ok := Find(LlamaTag, Vulkan, waspRoot, hiveRoot); ok {
		t.Fatal("an old build reused as current")
	}
	if _, ok := Find("b1", Vulkan, filepath.Join(waspRoot, "missing")); ok {
		t.Fatal("found nothing in a missing root")
	}
	if !TagAtLeast("b11279", "b10950") || TagAtLeast("b10950", "b11279") || TagAtLeast("local-b1", "b1") {
		t.Fatal("TagAtLeast")
	}
}

func TestServeArgs(t *testing.T) {
	samplers := map[string]float64{"top_p": 0.95, "temp": 0.7, "top_k": 40, "bogus": 1}
	join := func(a []string) string { return strings.Join(a, " ") }

	gpu := join(ServeArgs("/m/a.gguf", fit.Shape{Kind: fit.GPU, GPULayers: -1}, 8192, 2, samplers, ServeOptions{Port: 4500}))
	want := "-m /m/a.gguf --host 127.0.0.1 --port 4500 -c 16384 -np 2 -ngl 999 -fa on -ctk q8_0 -ctv q8_0 --jinja --temp 0.7 --top-k 40 --top-p 0.95"
	if gpu != want {
		t.Fatalf("gpu:\n got %s\nwant %s", gpu, want)
	}
	moe := join(ServeArgs("m", fit.Shape{Kind: fit.MoEOffload, GPULayers: -1, MoECPULayers: 30}, 4096, 1, nil, ServeOptions{}))
	if !strings.Contains(moe, "-ngl 999 --n-cpu-moe 30 ") || !strings.Contains(moe, "--port 8080") || !strings.Contains(moe, "-c 4096 -np 1") {
		t.Fatalf("moe: %s", moe)
	}
	part := join(ServeArgs("m", fit.Shape{Kind: fit.Partial, GPULayers: 15}, 8192, 1, nil, ServeOptions{}))
	if !strings.Contains(part, "-ngl 15 ") || strings.Contains(part, "n-cpu-moe") {
		t.Fatalf("partial: %s", part)
	}
	cpu := join(ServeArgs("m", fit.Shape{Kind: fit.CPU}, 8192, 0, nil, ServeOptions{}))
	if !strings.Contains(cpu, "-ngl 0 ") || !strings.Contains(cpu, "-np 1") {
		t.Fatalf("cpu: %s", cpu)
	}
	// Never beyond loopback unless asked.
	if !strings.Contains(gpu, "--host 127.0.0.1") {
		t.Fatal("not loopback")
	}
}
