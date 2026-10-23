package main

// Agent definitions and user config loading.
//
// Claude, Codex and Gemini are OFFICIAL vendor CLIs that you sign into with
// your own plan. Ensemble only launches the program and reads what it prints.
// It never reads, copies or reuses their login tokens, and never automates a
// website. Perplexity and DeepSeek use your own pay-as-you-go API keys.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type Agent struct {
	Label        string              `json:"label,omitempty"`
	Membership   string              `json:"membership,omitempty"`
	Type         string              `json:"type,omitempty"` // "" = CLI, "perplexity" = HTTP API
	Command      string              `json:"command,omitempty"`
	VersionArgs  []string            `json:"versionArgs,omitempty"`
	Install      string              `json:"install,omitempty"`
	Login        string              `json:"login,omitempty"`
	Modes        map[string][]string `json:"modes,omitempty"` // "{prompt}" = pass as argument, else stdin
	MaxArgPrompt int                 `json:"maxArgPrompt,omitempty"`
	AllowWrite   *bool               `json:"allowWrite,omitempty"`
	Enabled      *bool               `json:"enabled,omitempty"`
	KeyName      string              `json:"keyName,omitempty"`
	Endpoint     string              `json:"endpoint,omitempty"`
	Request      map[string]any      `json:"request,omitempty"`
	NoFiles      bool                `json:"noFiles,omitempty"`
	Env          map[string]string   `json:"env,omitempty"`
	UnsetEnv     []string            `json:"unsetEnv,omitempty"`
	// One-click setup on Windows: the vendor's official installer, and the
	// command that signs in (opened in a terminal window for you).
	InstallScript string `json:"installScript,omitempty"`
	LoginCmd      string `json:"loginCmd,omitempty"`
}

func (a *Agent) CanWrite() bool  { return a.AllowWrite == nil || *a.AllowWrite }
func (a *Agent) IsEnabled() bool { return a.Enabled == nil || *a.Enabled }

type Config struct {
	Agents          map[string]*Agent   `json:"agents"`
	Roles           map[string][]string `json:"roles"`
	Port            int                 `json:"port"`
	TimeoutMinutes  int                 `json:"timeoutMinutes"`
	CouncilRounds   int                 `json:"councilRounds"`
	MaxReviewRounds int                 `json:"maxReviewRounds"`
}

// AgentOrder is the display order; agents added in config.json come after, sorted.
var AgentOrder = []string{"claude", "codex", "gemini", "perplexity", "deepseek"}

func boolPtr(b bool) *bool { return &b }

var claudeArgs = map[string][]string{
	// Plain print mode: anything needing approval is refused, so it can only read.
	"read": {"-p", "--output-format", "text"},
	// Auto-accepts file edits only. Shell commands still need approval and are refused.
	"write": {"-p", "--output-format", "text", "--permission-mode", "acceptEdits"},
}

func defaultAgents() map[string]*Agent {
	return map[string]*Agent{
		"claude": {
			Label:         "Claude",
			Membership:    "Claude Pro / Max",
			Command:       "claude",
			VersionArgs:   []string{"--version"},
			Install:       "PowerShell: irm https://claude.ai/install.ps1 | iex",
			Login:         "Run `claude` once and choose your Claude Pro/Max account.",
			Modes:         cloneModes(claudeArgs),
			InstallScript: "irm https://claude.ai/install.ps1 | iex",
			LoginCmd:      "claude",
		},
		"codex": {
			Label:         "ChatGPT (Codex)",
			Membership:    "ChatGPT Plus / Pro",
			Command:       "codex",
			VersionArgs:   []string{"--version"},
			Install:       "PowerShell: irm https://chatgpt.com/codex/install.ps1 | iex",
			Login:         "Run `codex login` and choose \"Sign in with ChatGPT\".",
			InstallScript: "irm https://chatgpt.com/codex/install.ps1 | iex",
			LoginCmd:      "codex login",
			Modes: map[string][]string{
				"read":  {"exec", "--skip-git-repo-check", "-s", "read-only", "-"},
				"write": {"exec", "--skip-git-repo-check", "-s", "workspace-write", "-"},
			},
		},
		"gemini": {
			Label:         "Gemini (Antigravity)",
			Membership:    "Google AI Pro / Ultra",
			Command:       "agy",
			VersionArgs:   []string{"--version"},
			Install:       "PowerShell: irm https://antigravity.google/cli/install.ps1 | iex",
			Login:         "Run `agy` once and sign in with the Google account that has AI Pro.",
			InstallScript: "irm https://antigravity.google/cli/install.ps1 | iex",
			LoginCmd:      "agy",
			Modes: map[string][]string{
				"read": {"-p", "{prompt}", "--print-timeout", "20m"},
				// agy has no "edits only" mode, so writing means skipping all approvals,
				// shell included. Off unless you set "allowWrite": true for gemini.
				"write": {"-p", "{prompt}", "--print-timeout", "20m", "--dangerously-skip-permissions"},
			},
			// agy only takes the prompt as an argument; long prompts are moved into a file.
			MaxArgPrompt: 6000,
			AllowWrite:   boolPtr(false),
		},
		"perplexity": {
			Label:      "Perplexity",
			Membership: "Perplexity API (pay-as-you-go; Pro/Max plans do not include API use)",
			Type:       "perplexity",
			KeyName:    "perplexity",
			Endpoint:   "https://api.perplexity.ai/v1/agent",
			// "low" = fast web-searching model with sources (~$0.05–0.10 per request).
			Request:    map[string]any{"preset": "low"},
			NoFiles:    true, // searches the web but can't see your project folder
			Install:    "Create an API key at perplexity.ai/account/api and add credit.",
			Login:      "Click the chip in Ensemble, or run `ensemble-cli key perplexity <key>`.",
			AllowWrite: boolPtr(false),
		},
		"deepseek": {
			Label:      "DeepSeek",
			Membership: "DeepSeek API (pay-as-you-go; there is no DeepSeek membership)",
			// DeepSeek's own documented setup: Claude Code running on DeepSeek models.
			// It gets its own settings folder so it never touches your Claude login.
			Command:     "claude",
			VersionArgs: []string{"--version"},
			KeyName:     "deepseek",
			UnsetEnv:    []string{"ANTHROPIC_API_KEY"},
			Env: map[string]string{
				"ANTHROPIC_BASE_URL":   "https://api.deepseek.com/anthropic",
				"ANTHROPIC_AUTH_TOKEN": "{key}",
				// Values from DeepSeek's "Integrate with Claude Code" guide (Oct 2026).
				// For the bigger model set ANTHROPIC_MODEL to "deepseek-v4-pro" in your config.
				"ANTHROPIC_MODEL":                 "deepseek-flash[1m]",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":    "deepseek-flash[1m]",
				"ANTHROPIC_DEFAULT_SONNET_MODEL":  "deepseek-flash[1m]",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":   "deepseek-flash",
				"CLAUDE_CODE_SUBAGENT_MODEL":      "deepseek-flash",
				"CLAUDE_CODE_EFFORT_LEVEL":        "max",
				"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "786432",
				"CLAUDE_CONFIG_DIR":               "{home}/deepseek-claude",
			},
			Install: "Needs Claude Code installed (irm https://claude.ai/install.ps1 | iex) plus a key from platform.deepseek.com with some credit.",
			Login:   "Click the chip in Ensemble, or run `ensemble-cli key deepseek <key>`.",
			Modes:   cloneModes(claudeArgs),
		},
	}
}

func DefaultRoles() map[string][]string {
	return map[string][]string{
		// Build workflow
		"researcher": {"perplexity"},
		"planner":    {"claude", "gemini", "codex", "deepseek"},
		"builder":    {"codex", "claude", "deepseek", "gemini"},
		"reviewer":   {"gemini", "deepseek", "claude", "codex"},
		"fixer":      {"claude", "codex", "deepseek", "gemini"},
		// Council workflow
		"chair": {"claude", "gemini", "codex", "deepseek"},
	}
}

func cloneModes(m map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range m {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func HomeDir() string {
	if h := os.Getenv("ENSEMBLE_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".ensemble")
}

func ConfigPath() string { return filepath.Join(HomeDir(), "config.json") }

func EnsureHome() string {
	h := HomeDir()
	_ = os.MkdirAll(h, 0o700)
	return h
}

// LoadConfig merges ~/.ensemble/config.json over the defaults. Agent fields
// are merged individually, so a config can change one flag of one agent.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		Agents:          defaultAgents(),
		Roles:           DefaultRoles(),
		Port:            4317,
		TimeoutMinutes:  20,
		CouncilRounds:   2,
		MaxReviewRounds: 2,
	}
	if raw, err := os.ReadFile(ConfigPath()); err == nil {
		var user struct {
			Agents          map[string]json.RawMessage `json:"agents"`
			Roles           map[string][]string        `json:"roles"`
			Port            *int                       `json:"port"`
			TimeoutMinutes  *int                       `json:"timeoutMinutes"`
			CouncilRounds   *int                       `json:"councilRounds"`
			MaxReviewRounds *int                       `json:"maxReviewRounds"`
		}
		if err := json.Unmarshal(raw, &user); err != nil {
			return nil, fmt.Errorf("could not parse %s: %v", ConfigPath(), err)
		}
		for id, r := range user.Agents {
			a := cfg.Agents[id]
			if a == nil {
				a = &Agent{Label: id}
				cfg.Agents[id] = a
			}
			if err := json.Unmarshal(r, a); err != nil {
				return nil, fmt.Errorf("config agent %q: %v", id, err)
			}
		}
		for k, v := range user.Roles {
			cfg.Roles[k] = v
		}
		setInt := func(dst *int, v *int) {
			if v != nil {
				*dst = *v
			}
		}
		setInt(&cfg.Port, user.Port)
		setInt(&cfg.TimeoutMinutes, user.TimeoutMinutes)
		setInt(&cfg.CouncilRounds, user.CouncilRounds)
		setInt(&cfg.MaxReviewRounds, user.MaxReviewRounds)
	}
	applyMock(cfg)
	return cfg, nil
}

// applyMock is a test hook: every agent becomes this same program acting as a fake CLI.
func applyMock(cfg *Config) {
	exe := os.Getenv("ENSEMBLE_MOCK")
	if exe == "" {
		return
	}
	for id, a := range cfg.Agents {
		a.Command = exe
		a.VersionArgs = []string{id, "--version"}
		a.Modes = map[string][]string{"read": {id, "read"}, "write": {id, "write"}}
		a.AllowWrite = boolPtr(id != "perplexity")
		a.MaxArgPrompt = 0
		a.Type = ""
		a.KeyName = ""
		a.UnsetEnv = nil
		a.Env = map[string]string{"ENSEMBLE_AS_MOCK": "1"}
	}
}

func (c *Config) OrderedIDs() []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range AgentOrder {
		if _, ok := c.Agents[id]; ok {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	extra := []string{}
	for id := range c.Agents {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	return append(ids, extra...)
}

func (c *Config) Label(id string) string {
	if a := c.Agents[id]; a != nil && a.Label != "" {
		return a.Label
	}
	return id
}

// WriteStarterConfig creates a config.json showing the common settings.
func WriteStarterConfig() (string, bool, error) {
	EnsureHome()
	p := ConfigPath()
	if _, err := os.Stat(p); err == nil {
		return p, false, nil
	}
	starter := map[string]any{
		"roles":           DefaultRoles(),
		"timeoutMinutes":  20,
		"councilRounds":   2,
		"maxReviewRounds": 2,
		"agents":          map[string]any{"gemini": map[string]any{"allowWrite": false}},
	}
	b, _ := json.MarshalIndent(starter, "", "  ")
	return p, true, os.WriteFile(p, b, 0o600)
}

func itoa(n int) string { return strconv.Itoa(n) }
