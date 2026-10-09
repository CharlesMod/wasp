package wizard

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
)

// Run is the non-interactive install: Plan, then each stage in order with no
// window, printing "==> stage" and its lines to o.Stdout. A nil error is exit 0.
func Run(ctx context.Context, p Product, o Options) error {
	out := o.Stdout
	if out == nil {
		out = os.Stdout
	}
	if o.Root == "" {
		o.Root = profileRoot()
	}
	a := Answers{}
	for k, v := range o.Answers {
		a[k] = v
	}
	// every page's check runs, as the window would have run it
	c := Context{Profile: detectProfile(o), Root: o.Root, Answers: a}
	for _, pg := range p.Pages() {
		if pg.Check != nil {
			if err := pg.Check(c, a); err != nil {
				return fmt.Errorf("%s: %w", pg.Title, err)
			}
		}
	}
	stages, err := p.Plan(a)
	if err != nil {
		return err
	}
	for _, st := range stages {
		fmt.Fprintf(out, "==> %s\n", st.Name)
		step := &Step{
			say:  func(l string) { fmt.Fprintf(out, "    %s\n", l) },
			prog: func(done, total int64) {},
		}
		if err := st.Run(ctx, step); err != nil {
			return fmt.Errorf("%s: %w", st.Name, err)
		}
	}
	return nil
}

// Command is a product's main: it parses the usual flags and either serves
// the window or runs non-interactively. It returns the exit code.
//
//	--no-window       serve only and print the address
//	--answers FILE    a JSON object of answers; with --yes, install with no window
//	--yes             run non-interactively (needs --answers or none)
//	--root DIR        the install root
func Command(p Product, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(p.Info().Name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	noWindow := fs.Bool("no-window", false, "serve only; print the address")
	answers := fs.String("answers", "", "JSON file of answers")
	yes := fs.Bool("yes", false, "install now with no window, from --answers")
	root := fs.String("root", "", "install root")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	o := Options{Root: *root, NoWindow: *noWindow, Stdout: stdout}
	if *answers != "" {
		b, err := os.ReadFile(*answers)
		if err == nil {
			err = json.Unmarshal(b, &o.Answers)
		}
		if err != nil {
			fmt.Fprintf(stderr, "answers: %v\n", err)
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *yes {
		if err := Run(ctx, p, o); err != nil {
			fmt.Fprintf(stderr, "failed: %v\n", err)
			return 1
		}
		return 0
	}
	s, err := New(p, o)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "setup is at %s\n", s.URL())
	if !o.NoWindow {
		info := p.Info()
		open := o.Opener
		if open == nil {
			open = func(u string) error { return OpenWindow(u, info.WindowW, info.WindowH) }
		}
		if err := open(s.URL()); err != nil {
			fmt.Fprintf(stdout, "could not open a window (%v); open the address above in a browser\n", err)
		}
	}
	if err := s.Serve(ctx); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	return 0
}
