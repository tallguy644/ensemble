package main

// API keys for pay-as-you-go agents (Perplexity, DeepSeek).
// Read from environment variables first, then ~/.ensemble/keys.json
// (owner-only). Keys are only sent to that vendor's own endpoint; they never
// go into prompts or run logs.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var KeyEnv = map[string]string{
	"perplexity": "PERPLEXITY_API_KEY",
	"deepseek":   "DEEPSEEK_API_KEY",
}

func keysFile() string { return filepath.Join(HomeDir(), "keys.json") }

func readKeys() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(keysFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeKeys(m map[string]string) error {
	EnsureHome()
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(keysFile(), b, 0o600); err != nil {
		return err
	}
	return os.Chmod(keysFile(), 0o600)
}

func envName(name string) string {
	if e, ok := KeyEnv[name]; ok {
		return e
	}
	return strings.ToUpper(name) + "_API_KEY"
}

func GetKey(name string) string {
	if v := os.Getenv(envName(name)); v != "" {
		return v
	}
	return readKeys()[name]
}

func SetKey(name, value string) error {
	if _, ok := KeyEnv[name]; !ok {
		return errors.New(`unknown key "` + name + `"; use perplexity or deepseek`)
	}
	v := strings.TrimSpace(value)
	if len(v) < 10 || strings.ContainsAny(v, " \t\r\n") {
		return errors.New("that does not look like an API key")
	}
	m := readKeys()
	m[name] = v
	return writeKeys(m)
}

func RemoveKey(name string) error {
	m := readKeys()
	delete(m, name)
	return writeKeys(m)
}

func KeySource(name string) string {
	if os.Getenv(envName(name)) != "" {
		return "environment (" + envName(name) + ")"
	}
	if readKeys()[name] != "" {
		return "saved key"
	}
	return ""
}
