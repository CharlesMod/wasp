package catalog

import "testing"

func TestParseObjectAndArray(t *testing.T) {
	const one = `{"name":"a","hf_repo":"o/r","hf_file":"f.gguf","bytes":10,"kind":"dense","rank":1,"samplers":{"temp":0.7}}`
	for _, doc := range []string{`{"models":[` + one + `]}`, `[` + one + `]`} {
		c, err := Parse([]byte(doc))
		if err != nil || len(c.Models) != 1 {
			t.Fatalf("%v %v", c, err)
		}
		e := c.Models[0]
		if e.URL() != "https://huggingface.co/o/r/resolve/main/f.gguf" || e.MinContext() != DefaultMinCtx || e.Verifiable() || e.Samplers["temp"] != 0.7 {
			t.Fatalf("%+v", e)
		}
	}
}

func TestValidateRefusals(t *testing.T) {
	for name, doc := range map[string]string{
		"no name":   `[{"hf_repo":"o/r","hf_file":"f","bytes":1,"kind":"dense"}]`,
		"bad kind":  `[{"name":"a","hf_repo":"o/r","hf_file":"f","bytes":1,"kind":"sparse"}]`,
		"no file":   `[{"name":"a","hf_repo":"o/r","bytes":1,"kind":"moe"}]`,
		"no size":   `[{"name":"a","hf_repo":"o/r","hf_file":"f","kind":"moe"}]`,
		"duplicate": `[{"name":"a","hf_repo":"o/r","hf_file":"f","bytes":1,"kind":"moe"},{"name":"a","hf_repo":"o/r","hf_file":"f","bytes":1,"kind":"moe"}]`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestVerifiable(t *testing.T) {
	e := Entry{SHA256: "sha256:" + string(make([]byte, 0))}
	if e.Verifiable() {
		t.Fatal("empty")
	}
	e.SHA256 = "658a9e7e406deb06d0179755e3c14f6a82915a4be4962a2f92a64d948d2e572f"
	if !e.Verifiable() {
		t.Fatal("full digest")
	}
}
