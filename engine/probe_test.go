package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProbeErrorNamesAMissingLibraryAndItsPackage(t *testing.T) {
	out := "/x/llama-server: error while loading shared libraries: libgomp.so.1: cannot open shared object file: No such file or directory\n"
	err := probeError(out, errors.New("exit status 127"))
	for _, want := range []string{"libgomp.so.1", "libgomp1 on Debian and Ubuntu", "Retry"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q lacks %q", err, want)
		}
	}
	err = probeError("x: error while loading shared libraries: libfoo.so.9: cannot open", errors.New("exit status 127"))
	if !strings.Contains(err.Error(), "libfoo.so.9") {
		t.Fatal(err)
	}
	err = probeError("warming\nIllegal instruction\n", errors.New("signal: illegal instruction"))
	if !strings.Contains(err.Error(), "Illegal instruction") {
		t.Fatal(err)
	}
}

func TestProbeRunsTheServerWithItsLibraryPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for the server")
	}
	dir := t.TempDir()
	srv := filepath.Join(dir, "llama-server")
	script := "#!/bin/sh\n[ \"$1\" = --version ] && [ \"$LD_LIBRARY_PATH$DYLD_LIBRARY_PATH\" = \"" + dir + "\" ] && exit 0\n" +
		"echo \"$0: error while loading shared libraries: libgomp.so.1: cannot open shared object file\" >&2; exit 127\n"
	if err := os.WriteFile(srv, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LD_LIBRARY_PATH", "")
	t.Setenv("DYLD_LIBRARY_PATH", "")
	if err := Probe(context.Background(), srv); err != nil {
		t.Fatalf("a server that starts read as broken: %v", err)
	}
	if err := os.WriteFile(srv, []byte("#!/bin/sh\necho \"$0: error while loading shared libraries: libgomp.so.1: cannot open\" >&2\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Probe(context.Background(), srv); err == nil || !strings.Contains(err.Error(), "libgomp1") {
		t.Fatalf("want the missing library named, got %v", err)
	}
}
