// hello-setup is the smallest Product: three pages and two stages that only
// say lines. `go run ./examples/hello-setup` opens its window;
// `--no-window` serves only; `--yes --answers a.json` installs with no window.
package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/CharlesMod/wasp/wizard"
)

// Hello is the example product.
type Hello struct{}

func (Hello) Info() wizard.Info {
	return wizard.Info{Name: "Hello", Version: "0.1", Accent: "#4cc9f0", AccentBG: "#08202a", OnAccent: "#06121a"}
}

func (Hello) Pages() []wizard.Page {
	return []wizard.Page{
		{ID: "welcome", Title: "Welcome", Lede: "A tiny setup that only says things.",
			HTML: `<p>Nothing is installed. This shows the kit end to end.</p>`},
		{ID: "who", Title: "Who are you?", Lede: "Tell the setup what to call you.",
			HTML: `<label class="field" for="who-name">Your name</label>
<input type="text" id="who-name" autocomplete="off">
<p class="hint" id="who-machine"></p>`,
			Data: func(c wizard.Context) (any, error) {
				return map[string]any{"cpus": c.Profile.CPUs, "os": c.Profile.OS}, nil
			},
			Check: func(c wizard.Context, a wizard.Answers) error {
				if strings.TrimSpace(a.String("name")) == "" {
					return errors.New("Please type a name.")
				}
				return nil
			},
			Script: `function render(d, answers) {
  root.querySelector('#who-machine').textContent = d.cpus + ' processors on ' + d.os;
  if (answers && answers.name) root.querySelector('#who-name').value = answers.name;
}
function collect() { return { name: root.querySelector('#who-name').value.trim() }; }`},
		{ID: "done", Title: "Done", Lede: "",
			HTML: `<p class="ok">All set.</p>`},
	}
}

func (Hello) Plan(a wizard.Answers) ([]wizard.Stage, error) {
	name := strings.TrimSpace(a.String("name"))
	if name == "" {
		return nil, errors.New("no name was given")
	}
	return []wizard.Stage{
		{Name: "Saying hello", Run: func(ctx context.Context, s *wizard.Step) error {
			s.Say("Hello, " + name + ".")
			for i := int64(0); i <= 4; i++ {
				s.Progress(i, 4)
				select {
				case <-time.After(150 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}},
		{Name: "Saying goodbye", Run: func(ctx context.Context, s *wizard.Step) error {
			s.Say("Goodbye, " + name + ".")
			return nil
		}},
	}, nil
}

func main() {
	os.Exit(wizard.Command(Hello{}, os.Args[1:], os.Stdout, os.Stderr))
}
