package doctor

import (
	"io/fs"
	"strings"
	"testing"
)

func TestThePackageIsCarriedWhole(t *testing.T) {
	for _, f := range []string{"__init__.py", "protocol.py", "doctor.py", "cures.py", "looks.py", "platform.py", "casebook.py", "court.py"} {
		b, err := fs.ReadFile(Python, "waspdoctor/"+f)
		if err != nil || len(b) == 0 {
			t.Fatalf("waspdoctor/%s not carried: %v", f, err)
		}
	}
	_ = fs.WalkDir(Python, ".", func(p string, d fs.DirEntry, err error) error {
		if strings.Contains(p, "__pycache__") || strings.HasSuffix(p, ".pyc") {
			t.Errorf("bytecode carried: %s", p)
		}
		return nil
	})
}
