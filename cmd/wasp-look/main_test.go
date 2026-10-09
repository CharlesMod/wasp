package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookPrintsProfileAndTable(t *testing.T) {
	dir := t.TempDir()
	prof := filepath.Join(dir, "p.json")
	os.WriteFile(prof, []byte(`{"os":"linux","arch":"amd64","cpus":4,"ram":8589934592,"ram_avail":6442450944,"gpus":[]}`), 0o644)
	cat := filepath.Join(dir, "c.json")
	os.WriteFile(cat, []byte(`[{"name":"big","hf_repo":"o/r","hf_file":"f","bytes":20000000000,"kind":"dense","rank":1,
	 "facts":{"arch":"x","layers":10,"kv_layers":10,"kv_heads":4,"key_len":128,"val_len":128,"ctx_train":8192,"total_bytes":20000000000}}]`), 0o644)
	var out, errw bytes.Buffer
	if code := run([]string{cat, "--profile", prof}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	for _, want := range []string{"gpu       none found", "LOCKED", "no model can run"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if run(nil, &out, &errw) != 2 {
		t.Error("no usage error")
	}
}
