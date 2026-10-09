package service

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

type call struct {
	name string
	args []string
}

type fake struct {
	calls []call
	out   map[string]string // "name verb" -> output
	fail  map[string]bool
}

func (f *fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name, args})
	key := name
	if len(args) > 0 {
		key += " " + args[0]
		if args[0] == "--user" && len(args) > 1 {
			key += " " + args[1]
		}
	}
	if f.fail[key] {
		return []byte("refused: " + key), errors.New("exit 1")
	}
	return []byte(f.out[key]), nil
}

func (f *fake) has(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c.name+" "+strings.Join(c.args, " "), sub) {
			return true
		}
	}
	return false
}

func spec(t *testing.T) Spec {
	return Spec{
		Name: "demo", Description: "Demo & service",
		Exec:    []string{"/opt/demo bin/demo", "--port", "8080", `say "hi" 100%`},
		Env:     map[string]string{"B": "2", "A": "x y"},
		WorkDir: t.TempDir(), RestartOnFailure: true, LogFile: filepath.Join(t.TempDir(), "log", "demo.log"),
	}
}

func mgr(t *testing.T, goos string, f *fake) *Manager {
	d := t.TempDir()
	return &Manager{GOOS: goos, Home: d, StateDir: filepath.Join(d, "state"), UID: 501, Run: f.run}
}

func TestUnitFields(t *testing.T) {
	s := spec(t)
	u := Unit(s, false)
	for _, want := range []string{
		`ExecStart="/opt/demo bin/demo" "--port" "8080" "say \"hi\" 100%%"`,
		`Environment="A=x y"`, `Environment="B=2"`, "Restart=on-failure",
		"WorkingDirectory=" + s.WorkDir, "StandardOutput=append:" + s.LogFile,
		"WantedBy=default.target", "Description=Demo & service",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Index(u, `"A=x y"`) > strings.Index(u, `"B=2"`) {
		t.Error("env must be sorted")
	}
	s.RestartOnFailure = false
	if !strings.Contains(Unit(s, true), "Restart=no") || !strings.Contains(Unit(s, true), "multi-user.target") {
		t.Error("no-restart or system target")
	}
}

func TestPlistFields(t *testing.T) {
	s := spec(t)
	p := Plist(s)
	if err := xml.Unmarshal([]byte(p), new(struct{})); err != nil {
		t.Fatalf("plist is not XML: %v", err)
	}
	for _, want := range []string{
		"<string>wasp.demo</string>", "<string>/opt/demo bin/demo</string>",
		"<string>say &#34;hi&#34; 100%</string>", "<key>A</key>", "<string>x y</string>",
		"<key>RunAtLoad</key>", "<key>SuccessfulExit</key>", "<key>StandardOutPath</key>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
	s.RestartOnFailure = false
	if !strings.Contains(Plist(s), "<key>KeepAlive</key>\n\t<false/>") {
		t.Error("no keepalive expected")
	}
}

func TestTaskXMLAndWrapper(t *testing.T) {
	s := spec(t)
	x := TaskXML(s, `C:\w\demo.ps1`)
	if err := xml.Unmarshal([]byte(strings.Replace(x, "UTF-16", "UTF-8", 1)), new(struct{})); err != nil {
		t.Fatalf("task xml: %v", err)
	}
	for _, want := range []string{"<LogonTrigger>", "<RunLevel>LeastPrivilege</RunLevel>", "<Hidden>true</Hidden>",
		"-WindowStyle Hidden", `demo.ps1`, "<RestartOnFailure>"} {
		if !strings.Contains(x, want) {
			t.Errorf("task lacks %q", want)
		}
	}
	w := WindowsWrapper(s)
	for _, want := range []string{"$env:A = 'x y'", "while ($true)", "$LASTEXITCODE -eq 0",
		"& '/opt/demo bin/demo' '--port' '8080'", "*>>", `'say "hi" 100%'`} {
		if !strings.Contains(w, want) {
			t.Errorf("wrapper lacks %q:\n%s", want, w)
		}
	}
	s.RestartOnFailure = false
	if strings.Contains(WindowsWrapper(s), "while") {
		t.Error("no loop without restart")
	}
	b := UTF16LE("A")
	if len(b) != 4 || b[0] != 0xff || b[2] != 'A' {
		t.Errorf("utf16: %v", b)
	}
}

func TestInstallLinuxUser(t *testing.T) {
	f := &fake{}
	m := mgr(t, "linux", f)
	res, err := m.Install(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(m.Home, ".config", "systemd", "user", "demo.service")
	if _, err := os.Stat(p); err != nil || len(res.Files) != 1 || res.Files[0] != p {
		t.Fatalf("unit not written: %v %v", err, res)
	}
	for _, want := range []string{"systemctl --user daemon-reload", "systemctl --user enable --now demo.service", "loginctl enable-linger"} {
		if !f.has(want) {
			t.Errorf("missing command %q in %v", want, f.calls)
		}
	}
	if len(res.Notes) != 0 {
		t.Errorf("notes: %v", res.Notes)
	}
}

func TestInstallLinuxLingerFailureIsSaid(t *testing.T) {
	f := &fake{fail: map[string]bool{"loginctl enable-linger": true}}
	res, err := mgr(t, "linux", f).Install(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "only while you are logged in") {
		t.Errorf("notes: %v", res.Notes)
	}
}

func TestInstallLinuxEnableFailureIsAnError(t *testing.T) {
	f := &fake{fail: map[string]bool{"systemctl --user enable": true}}
	if _, err := mgr(t, "linux", f).Install(context.Background(), spec(t)); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallLinuxRootUsesSystemUnitNotUserDirs(t *testing.T) {
	f := &fake{}
	m := mgr(t, "linux", f)
	m.Root = true
	s := spec(t)
	s.User = true // forced per-user even as root
	if _, err := m.Install(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if !f.has("systemctl --user enable") {
		t.Error("User must force a user unit")
	}
	if m.unitPath("x", true) != "/etc/systemd/system/x.service" {
		t.Error("system path")
	}
}

func TestInstallDarwin(t *testing.T) {
	f := &fake{}
	m := mgr(t, "darwin", f)
	res, err := m.Install(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(m.Home, "Library", "LaunchAgents", "wasp.demo.plist")
	if len(res.Files) != 1 || res.Files[0] != p {
		t.Fatalf("files: %v", res.Files)
	}
	if !f.has("launchctl bootstrap gui/501 " + p) {
		t.Errorf("calls: %v", f.calls)
	}
}

func TestInstallWindows(t *testing.T) {
	f := &fake{}
	m := mgr(t, "windows", f)
	res, err := m.Install(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("files: %v", res.Files)
	}
	raw, _ := os.ReadFile(res.Files[1])
	if raw[0] != 0xff || raw[1] != 0xfe {
		t.Error("task xml must be UTF-16LE with a BOM")
	}
	if !f.has("schtasks /create /tn demo /xml") || !f.has("schtasks /run /tn demo") {
		t.Errorf("calls: %v", f.calls)
	}
}

func TestStartStopUninstall(t *testing.T) {
	ctx := context.Background()
	f := &fake{}
	m := mgr(t, "linux", f)
	if _, err := m.Install(ctx, spec(t)); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, "demo"); err != nil || !f.has("systemctl --user start demo.service") {
		t.Fatalf("start: %v %v", err, f.calls)
	}
	if err := m.Stop(ctx, "demo"); err != nil || !f.has("systemctl --user stop demo.service") {
		t.Fatalf("stop: %v", err)
	}
	if err := m.Uninstall(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.unitPath("demo", false)); !os.IsNotExist(err) {
		t.Error("unit not removed")
	}
	for _, g := range []string{"darwin", "windows"} {
		f := &fake{}
		m := mgr(t, g, f)
		if _, err := m.Install(ctx, spec(t)); err != nil {
			t.Fatal(err)
		}
		if err := m.Start(ctx, "demo"); err != nil {
			t.Fatal(err)
		}
		if err := m.Stop(ctx, "demo"); err != nil {
			t.Fatal(err)
		}
		if err := m.Uninstall(ctx, "demo"); err != nil {
			t.Fatal(err)
		}
		if g == "windows" && !f.has("schtasks /delete /tn demo /f") {
			t.Error("task not deleted")
		}
		if g == "darwin" && !f.has("launchctl bootout gui/501/wasp.demo") {
			t.Error("job not booted out")
		}
	}
}

func TestStatusParsing(t *testing.T) {
	st := ParseSystemctlShow("LoadState=loaded\nActiveState=active\nSubState=running\nExecMainStatus=0\nResult=success\n")
	if !st.Running || st.Failed || !st.Installed {
		t.Errorf("running: %+v", st)
	}
	st = ParseSystemctlShow("LoadState=loaded\nActiveState=failed\nSubState=failed\nExecMainStatus=3\nResult=exit-code\n")
	if st.Running || !st.Failed || st.LastExit != "3" {
		t.Errorf("failed: %+v", st)
	}
	st = ParseSystemctlShow("LoadState=not-found\nActiveState=inactive\nSubState=dead\n")
	if st.Installed {
		t.Errorf("not found: %+v", st)
	}
	st = ParseLaunchctlPrint("gui/501/wasp.demo = {\n\tstate = running\n\tlast exit code = (never exited)\n}")
	if !st.Running || st.Failed {
		t.Errorf("launchd running: %+v", st)
	}
	st = ParseLaunchctlPrint("gui/501/wasp.demo = {\n\tstate = waiting\n\tlast exit code = 2\n}")
	if st.Running || !st.Failed || st.LastExit != "2" {
		t.Errorf("launchd failed: %+v", st)
	}
	// a running agent's nested blocks print a state of their own (live, macOS 26)
	st = ParseLaunchctlPrint("gui/501/wasp.demo = {\n\tactive count = 1\n\tstate = running\n\tpid = 4242\n" +
		"\tevent triggers = {\n\t\tcom.apple.x => {\n\t\t\tstate = active\n\t\t}\n\t}\n" +
		"\tendpoints = {\n\t\t\"wasp.demo\" = {\n\t\t\tstate = active\n\t\t}\n\t}\n\tlast exit code = (never exited)\n}")
	if !st.Running || st.Failed || st.Detail != "running" {
		t.Errorf("a nested state read as the service's: %+v", st)
	}
	st = ParseSchtasksQuery("TaskName: \\demo\r\nStatus:                                Running\r\nLast Result:                          267009\r\n")
	if !st.Running || st.Failed {
		t.Errorf("schtasks running: %+v", st)
	}
	st = ParseSchtasksQuery("Status:   Ready\nLast Result:   1\n")
	if st.Running || !st.Failed || st.LastExit != "1" {
		t.Errorf("schtasks failed: %+v", st)
	}
}

func TestStatusUsesRunner(t *testing.T) {
	f := &fake{out: map[string]string{"systemctl --user show": "LoadState=loaded\nActiveState=active\nSubState=running\n"}}
	st, err := mgr(t, "linux", f).Status(context.Background(), "demo")
	if err != nil || !st.Running {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestValidate(t *testing.T) {
	for _, s := range []Spec{{Name: "../x", Exec: []string{"a"}}, {Name: "ok"}, {Name: "", Exec: []string{"a"}}} {
		if _, err := mgr(t, "linux", &fake{}).Install(context.Background(), s); err == nil {
			t.Errorf("%+v should be refused", s)
		}
	}
}

// A real systemd user unit is exercised only on request.
func TestLiveSystemd(t *testing.T) {
	if os.Getenv("WASP_LIVE_SERVICE") != "1" {
		t.Skip("set WASP_LIVE_SERVICE=1 to install a real user unit")
	}
	ctx := context.Background()
	m := Default()
	s := Spec{Name: "wasp-live-test", Exec: []string{"/bin/sleep", "300"}, User: true}
	if _, err := m.Install(ctx, s); err != nil {
		t.Fatal(err)
	}
	defer m.Uninstall(ctx, s.Name)
	st, err := m.Status(ctx, s.Name)
	if err != nil || !st.Running {
		t.Fatalf("%+v %v", st, err)
	}
}

// Live on Windows 11: schtasks /end left the task's program and its children
// running, holding their log, so the install folder could not be removed.
func TestWindowsStopAndUninstallEndTheWholeTree(t *testing.T) {
	ctx := context.Background()
	f := &fake{}
	m := mgr(t, "windows", f)
	if _, err := m.Install(ctx, spec(t)); err != nil {
		t.Fatal(err)
	}
	tree := func() string {
		for _, c := range f.calls {
			if c.name == "powershell.exe" && len(c.args) == 4 && c.args[2] == "-EncodedCommand" {
				b, err := base64.StdEncoding.DecodeString(c.args[3])
				if err != nil {
					t.Fatal(err)
				}
				u := make([]uint16, len(b)/2)
				for i := range u {
					u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
				}
				return string(utf16.Decode(u))
			}
		}
		return ""
	}
	if err := m.Stop(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	ps := tree()
	if !strings.Contains(ps, "taskkill.exe /T /F") || !strings.Contains(ps, ".Contains('"+m.wrapperPath("demo")+"')") {
		t.Fatalf("stop must end the wrapper named by its own script, and its tree: %q", ps)
	}
	f.calls = nil
	if err := m.Uninstall(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if tree() == "" || !f.has("schtasks /delete /tn demo /f") {
		t.Fatalf("uninstall: %v", f.calls)
	}
	if _, err := os.Stat(m.wrapperPath("demo")); !os.IsNotExist(err) {
		t.Fatal("the wrapper script stays behind")
	}
}

func TestUninstallingATaskThatIsNotThereIsNotAnError(t *testing.T) {
	f := &fake{fail: map[string]bool{"schtasks /query": true, "schtasks /delete": true}}
	m := mgr(t, "windows", f)
	if err := m.Uninstall(context.Background(), "never-installed"); err != nil {
		t.Fatalf("a task never installed is already gone: %v", err)
	}
	if f.has("schtasks /delete") {
		t.Fatal("nothing to delete")
	}
}
