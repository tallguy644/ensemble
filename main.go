// Ensemble: Claude, ChatGPT (Codex), Gemini, Perplexity and DeepSeek working
// together on your projects. One self-contained program, no runtime needed.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"
)

// guiBuild is set to "1" for Ensemble.exe (the double-click app) at build time.
var guiBuild = ""

var version = "0.3.0"

const helpText = `Ensemble: Claude, ChatGPT (Codex), Gemini, Perplexity and DeepSeek working together.
Claude, ChatGPT and Gemini run on the memberships you already pay for; Perplexity
and DeepSeek use pay-as-you-go API keys.

Usage:
  ensemble-cli                                Open the Ensemble app
  ensemble-cli ui [--port 4317] [--no-open]   Run the dashboard in your browser instead
  ensemble-cli doctor                         Check which AIs are installed and ready
  ensemble-cli council "question" [options]   All models answer, critique each other, then merge
      --agents claude,codex,gemini,perplexity,deepseek   --rounds 2   --chair claude
      --file notes.md
  ensemble-cli build <folder> "task" [options] Research → plan → build → review → fix, on a new git branch
      --researcher perplexity --planner claude --builder codex --reviewer gemini --fixer claude
      --rounds 2   --test "npm test"   --allow-dirty   --no-branch   --no-research
  ensemble-cli key <perplexity|deepseek> <key> Save an API key (or --remove)
  ensemble-cli config                          Create/show your config file
  ensemble-cli version

Flags: -v / --verbose streams each model's raw output.`

type cliArgs struct {
	pos   []string
	flags map[string]string // "true"/"false" for switches
}

func parseArgs(argv []string) cliArgs {
	a := cliArgs{flags: map[string]string{}}
	for i := 0; i < len(argv); i++ {
		s := argv[i]
		switch {
		case strings.HasPrefix(s, "--no-"):
			a.flags[s[5:]] = "false"
		case s == "-v":
			a.flags["verbose"] = "true"
		case strings.HasPrefix(s, "--"):
			k := s[2:]
			if kk, v, ok := strings.Cut(k, "="); ok {
				a.flags[kk] = v
			} else if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				a.flags[k] = argv[i+1]
				i++
			} else {
				a.flags[k] = "true"
			}
		default:
			a.pos = append(a.pos, s)
		}
	}
	return a
}

func (a cliArgs) str(k string) string {
	v := a.flags[k]
	if v == "true" || v == "false" {
		return ""
	}
	return v
}

type palette struct{ on bool }

func (p palette) wrap(code, s string) string {
	if !p.on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (p palette) dim(s string) string    { return p.wrap("2", s) }
func (p palette) bold(s string) string   { return p.wrap("1", s) }
func (p palette) green(s string) string  { return p.wrap("32", s) }
func (p palette) red(s string) string    { return p.wrap("31", s) }
func (p palette) yellow(s string) string { return p.wrap("33", s) }
func (p palette) cyan(s string) string   { return p.wrap("36", s) }

var c palette

func main() {
	RefreshPath()
	args := os.Args[1:]
	if guiBuild == "1" || len(args) == 0 || args[0] == "app" {
		if err := runApp(); err != nil {
			ShowError("Ensemble couldn't start", err.Error())
			os.Exit(1)
		}
		return
	}
	c = palette{on: EnableConsoleColors() && os.Getenv("NO_COLOR") == ""}
	if err := runCLI(args); err != nil {
		fmt.Fprintln(os.Stderr, c.red(err.Error()))
		os.Exit(1)
	}
}

// runApp starts the local server and shows it in an app window. If the
// window can't be created (no WebView2), it falls back to the browser.
func runApp() error {
	EnsureHome()
	srv, err := NewServer(0)
	if err != nil {
		return err
	}
	go func() { _ = srv.Serve() }()
	defer srv.Close()
	err = RunWindow(srv.URL)
	if err == nil {
		CancelAllRuns()
		return nil
	}
	if err != ErrNoWindow {
		return err
	}
	// Fallback: browser tab. Keep running while the page is open.
	OpenBrowser(srv.URL)
	if guiBuild != "1" {
		fmt.Printf("Ensemble is running at:\n\n  %s\n\nKeep this window open. Press Ctrl+C to stop.\n", srv.URL)
	}
	waitForIdle()
	return nil
}

// waitForIdle exits once the page has been closed (no pings) and nothing is running.
func waitForIdle() {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-interrupt:
			CancelAllRuns()
			return
		case <-t.C:
			if time.Since(LastPing()) > 3*time.Minute && !AnyRunning() {
				return
			}
		}
	}
}

func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	PrepareCmd(cmd)
	_ = cmd.Start()
}

func runCLI(argv []string) error {
	cmd := argv[0]
	a := parseArgs(argv[1:])
	verbose := a.flags["verbose"] == "true"
	switch cmd {
	case "help", "-h", "--help":
		fmt.Println(helpText)
	case "version", "--version":
		fmt.Println("Ensemble " + version)
	case "doctor":
		return doctor()
	case "config":
		p, created, err := WriteStarterConfig()
		if err != nil {
			return err
		}
		if created {
			fmt.Println("Created " + p)
		} else {
			fmt.Println(p)
		}
	case "key":
		if len(a.pos) == 0 {
			fmt.Println("Usage: ensemble-cli key <perplexity|deepseek> <key>   (or --remove)")
			return nil
		}
		name := a.pos[0]
		if a.flags["remove"] == "true" {
			if err := RemoveKey(name); err != nil {
				return err
			}
			fmt.Printf("Removed the saved %s key.\n", name)
			return nil
		}
		if len(a.pos) < 2 {
			src := KeySource(name)
			if src == "" {
				src = "no key set"
			}
			fmt.Printf("%s: %s\n", name, src)
			return nil
		}
		if err := SetKey(name, a.pos[1]); err != nil {
			return err
		}
		fmt.Printf("Saved your %s key (only readable by your user account).\n", name)
	case "ui":
		port := 4317
		if cfg, err := LoadConfig(); err == nil {
			port = cfg.Port
		}
		if p := a.str("port"); p != "" {
			fmt.Sscanf(p, "%d", &port)
		}
		srv, err := NewServer(port)
		if err != nil {
			if strings.Contains(err.Error(), "address already in use") || strings.Contains(err.Error(), "Only one usage") {
				return fmt.Errorf("Port %d is busy. Try: ensemble-cli ui --port %d", port, port+1)
			}
			return err
		}
		go func() { _ = srv.Serve() }()
		fmt.Printf("%s is running at:\n\n  %s\n\nKeep this window open. Press Ctrl+C to stop.\n", c.bold("Ensemble dashboard"), c.cyan(srv.URL))
		if a.flags["open"] != "false" {
			OpenBrowser(srv.URL)
		}
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		<-interrupt
		CancelAllRuns()
		srv.Close()
	case "council":
		var atts []Attachment
		if f := a.str("file"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			atts = append(atts, Attachment{Name: f, Text: string(b)})
		}
		var agents []string
		if s := a.str("agents"); s != "" {
			for _, x := range strings.Split(s, ",") {
				agents = append(agents, strings.TrimSpace(x))
			}
		}
		rounds := 0
		fmt.Sscanf(a.str("rounds"), "%d", &rounds)
		run, err := StartCouncil(CouncilRequest{Question: strings.Join(a.pos, " "), Agents: agents, Rounds: flexInt(rounds), Chair: a.str("chair"), Attachments: atts})
		if err != nil {
			return err
		}
		return followAndPrint(run, verbose, "Saved in ")
	case "build":
		if len(a.pos) < 2 {
			return fmt.Errorf(`Usage: ensemble-cli build <folder> "what to build"`)
		}
		roles := map[string]string{}
		for _, r := range []string{"researcher", "planner", "builder", "reviewer", "fixer"} {
			if v := a.str(r); v != "" {
				roles[r] = v
			}
		}
		req := BuildRequest{
			Dir: a.pos[0], Task: strings.Join(a.pos[1:], " "), Roles: roles, TestCommand: a.str("test"),
			AllowDirty: a.flags["allow-dirty"] == "true", NoBranch: a.flags["branch"] == "false", NoResearch: a.flags["research"] == "false",
		}
		if s := a.str("rounds"); s != "" {
			n := 0
			fmt.Sscanf(s, "%d", &n)
			fi := flexInt(n)
			req.MaxReviewRounds = &fi
		}
		run, err := StartBuild(req)
		if err != nil {
			return err
		}
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		go func() {
			<-interrupt
			fmt.Println(c.yellow("\nCancelling..."))
			run.Cancel()
		}()
		return followAndPrint(run, verbose, "Full log: ")
	default:
		fmt.Println(helpText)
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

func doctor() error {
	fmt.Println(c.bold("Checking your AIs...") + "\n")
	list, err := AgentStatuses(true)
	if err != nil {
		return err
	}
	ready := 0
	for _, s := range list {
		switch s.Status {
		case "ready":
			ready++
			extra := ""
			if !s.CanWrite {
				if s.NoFiles {
					extra = c.dim("  (research and answers only)")
				} else {
					extra = c.dim("  (review/answer only; set allowWrite to let it edit files)")
				}
			}
			fmt.Printf("%s %s %s%s\n", c.green("✔"), c.bold(s.Label), c.dim(s.Version), extra)
		case "needs-key":
			fmt.Printf("%s %s — needs an API key %s\n", c.yellow("●"), c.bold(s.Label), c.dim("("+s.Membership+")"))
			fmt.Printf("    %s\n    Then: ensemble-cli key %s <your-key>\n", s.Install, s.KeyName)
		case "disabled":
			fmt.Printf("%s %s %s\n", c.dim("–"), s.Label, c.dim("(disabled in config)"))
		default:
			fmt.Printf("%s %s %s — %s\n", c.red("✘"), c.bold(s.Label), c.dim("("+s.Membership+")"), s.Detail)
			fmt.Printf("    Install: %s\n    Sign in: %s\n", s.Install, s.Login)
		}
	}
	fmt.Printf("\n%d of %d ready.\n", ready, len(list))
	fmt.Println(c.dim("Sign-in can only be confirmed by a real request; if a model says it is signed out during a run, Ensemble skips it and tells you."))
	return nil
}

func followAndPrint(run *Run, verbose bool, savedLabel string) error {
	wake := run.Subscribe()
	defer run.Unsubscribe(wake)
	titles := map[string]string{}
	next := 0
	for {
		for _, e := range run.EventsFrom(next) {
			next++
			switch e.Type {
			case "phase":
				d := ""
				if e.Detail != "" {
					d = c.dim(" — " + e.Detail)
				}
				fmt.Printf("\n%s %s%s\n", c.cyan("■"), c.bold(e.Name), d)
			case "step-start":
				titles[e.StepID] = e.Title + " · " + e.Label
				extra := ""
				if e.Mode == "write" {
					extra = c.dim(" (editing files)")
				}
				fmt.Printf("  %s %s%s\n", c.dim("…"), titles[e.StepID], extra)
			case "chunk":
				if verbose {
					fmt.Print(c.dim(e.Text))
				}
			case "step-end":
				mark, tail := c.green("✔"), ""
				if e.OK == nil || !*e.OK {
					mark, tail = c.red("✘"), " — "+e.Error
				}
				fmt.Printf("  %s %s %s%s\n", mark, titles[e.StepID], c.dim(fmt.Sprintf("%ds", e.Ms/1000)), tail)
			case "verdict":
				if e.Verdict == "approved" {
					fmt.Printf("  %s by %s\n", c.green("→ approved"), e.By)
				} else {
					fmt.Printf("  %s by %s\n", c.yellow("→ "+e.Verdict), e.By)
				}
			case "note":
				mark := c.dim("i")
				if e.Level == "warn" {
					mark = c.yellow("!")
				}
				fmt.Printf("  %s %s\n", mark, e.Text)
			case "done":
				fmt.Printf("\n%s\n%s\n%s\n", strings.Repeat("─", 60), e.Result.Markdown, c.dim(savedLabel+run.RunDir))
				return nil
			case "error":
				return fmt.Errorf("\n%s", e.Message)
			}
		}
		<-wake
	}
}
