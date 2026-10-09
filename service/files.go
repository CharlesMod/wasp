package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

func sortedEnv(env map[string]string) []string {
	k := make([]string, 0, len(env))
	for key := range env {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

// systemdQuote quotes one word of ExecStart.
func systemdQuote(w string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(w) + `"`
}

// Unit is the systemd unit for a spec. A user unit wants default.target; a
// system unit wants multi-user.target.
func Unit(s Spec, system bool) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	desc := s.Description
	if desc == "" {
		desc = s.Name
	}
	fmt.Fprintf(&b, "Description=%s\n", strings.ReplaceAll(desc, "\n", " "))
	b.WriteString("After=network-online.target\n\n[Service]\nType=simple\n")
	words := make([]string, len(s.Exec))
	for i, w := range s.Exec {
		words[i] = systemdQuote(w)
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(words, " "))
	if s.WorkDir != "" {
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", s.WorkDir)
	}
	for _, k := range sortedEnv(s.Env) {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(k+"="+s.Env[k]))
	}
	if s.RestartOnFailure {
		b.WriteString("Restart=on-failure\nRestartSec=5\n")
	} else {
		b.WriteString("Restart=no\n")
	}
	if s.LogFile != "" {
		fmt.Fprintf(&b, "StandardOutput=append:%s\nStandardError=append:%s\n", s.LogFile, s.LogFile)
	}
	target := "default.target"
	if system {
		target = "multi-user.target"
	}
	fmt.Fprintf(&b, "\n[Install]\nWantedBy=%s\n", target)
	return b.String()
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Plist is the launchd property list for a spec.
func Plist(s Spec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", xmlEsc(Label(s.Name)))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, w := range s.Exec {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlEsc(w))
	}
	b.WriteString("\t</array>\n")
	if s.WorkDir != "" {
		fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t<string>%s</string>\n", xmlEsc(s.WorkDir))
	}
	if len(s.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range sortedEnv(s.Env) {
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlEsc(k), xmlEsc(s.Env[k]))
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	if s.RestartOnFailure {
		b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	} else {
		b.WriteString("\t<key>KeepAlive</key>\n\t<false/>\n")
	}
	if s.LogFile != "" {
		fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n",
			xmlEsc(s.LogFile), xmlEsc(s.LogFile))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// WindowsWrapper is the PowerShell script the task runs: it sets the
// environment, runs the program, and (with RestartOnFailure) runs it again
// after a short backoff whenever it exits non-zero.
func WindowsWrapper(s Spec) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Continue'\r\n")
	for _, k := range sortedEnv(s.Env) {
		fmt.Fprintf(&b, "$env:%s = %s\r\n", k, psQuote(s.Env[k]))
	}
	if s.WorkDir != "" {
		fmt.Fprintf(&b, "Set-Location -LiteralPath %s\r\n", psQuote(s.WorkDir))
	}
	args := make([]string, 0, len(s.Exec)-1)
	for _, a := range s.Exec[1:] {
		args = append(args, psQuote(a))
	}
	call := "& " + psQuote(s.Exec[0])
	if len(args) > 0 {
		call += " " + strings.Join(args, " ")
	}
	if s.LogFile != "" {
		call += " *>> " + psQuote(s.LogFile)
	}
	if s.RestartOnFailure {
		b.WriteString("while ($true) {\r\n  " + call + "\r\n  if ($LASTEXITCODE -eq 0) { break }\r\n  Start-Sleep -Seconds 5\r\n}\r\n")
	} else {
		b.WriteString(call + "\r\n")
	}
	return b.String()
}

// TaskXML is the Task Scheduler definition: at logon, limited rights, hidden,
// running the wrapper script.
func TaskXML(s Spec, wrapper string) string {
	desc := s.Description
	if desc == "" {
		desc = s.Name
	}
	args := fmt.Sprintf(`-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "%s"`, wrapper)
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>` + xmlEsc(desc) + `</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Hidden>true</Hidden>
    <Enabled>true</Enabled>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>powershell.exe</Command>
      <Arguments>` + xmlEsc(args) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

func kv(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if i := strings.IndexByte(l, '='); i > 0 {
			m[strings.TrimSpace(l[:i])] = strings.TrimSpace(l[i+1:])
		}
	}
	return m
}

// ParseSystemctlShow reads `systemctl show -p ...` output.
func ParseSystemctlShow(out string) State {
	m := kv(out)
	st := State{Installed: m["LoadState"] != "not-found" && m["LoadState"] != "",
		Detail: m["ActiveState"]}
	st.Running = m["ActiveState"] == "active" && m["SubState"] == "running"
	st.Failed = m["ActiveState"] == "failed" || m["Result"] == "exit-code"
	if code := m["ExecMainStatus"]; code != "" && code != "0" || st.Failed {
		st.LastExit = m["ExecMainStatus"]
	}
	if st.Detail != "" && m["SubState"] != "" {
		st.Detail += " (" + m["SubState"] + ")"
	}
	return st
}

// ParseLaunchctlPrint reads `launchctl print gui/UID/label` output.
func ParseLaunchctlPrint(out string) State {
	st := State{Installed: true}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "state = "):
			st.Detail = strings.TrimPrefix(l, "state = ")
			st.Running = st.Detail == "running"
		case strings.HasPrefix(l, "last exit code = "):
			st.LastExit = strings.TrimPrefix(l, "last exit code = ")
			if st.LastExit != "0" && st.LastExit != "(never exited)" && !st.Running {
				st.Failed = true
			}
		}
	}
	return st
}

// ParseSchtasksQuery reads `schtasks /query /fo LIST /v` output.
func ParseSchtasksQuery(out string) State {
	st := State{Installed: true}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "Status:"):
			st.Detail = strings.TrimSpace(strings.TrimPrefix(l, "Status:"))
			st.Running = st.Detail == "Running"
		case strings.HasPrefix(l, "Last Result:"):
			st.LastExit = strings.TrimSpace(strings.TrimPrefix(l, "Last Result:"))
			if st.LastExit != "0" && st.LastExit != "267009" && st.LastExit != "267011" && !st.Running {
				st.Failed = true // 267009 running, 267011 never run
			}
		}
	}
	return st
}

// UTF16LE encodes s as UTF-16 little-endian with a byte order mark, the
// encoding Task Scheduler's XML declares.
func UTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2+2*len(u))
	out[0], out[1] = 0xff, 0xfe
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}
