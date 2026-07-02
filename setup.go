package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var stdinReader = bufio.NewReader(os.Stdin)

func askYN(prompt string, def bool) bool {
	d := "Y/n"
	if !def {
		d = "y/N"
	}
	fmt.Printf("  %-42s [%s] ", prompt, d)
	line, _ := stdinReader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

func askInt(prompt string, def int) int {
	fmt.Printf("  %-42s [%d] ", prompt, def)
	line, _ := stdinReader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	if n, err := strconv.Atoi(line); err == nil && n > 0 {
		return n
	}
	return def
}

func runSetup() {
	if _, err := exec.LookPath("qwen"); err != nil {
		fatalf("qwen-code not found — install it first:\n  npm install -g @qwen-code/qwen-code")
	}
	upstream, err := detectUpstream()
	if err != nil {
		fatalf("setup: %v", err)
	}

	fmt.Println("First run — quick setup:")
	fmt.Printf("  ✓ upstream detected: %s\n", upstream)
	hide := askYN("Hide <think> reasoning?", false)
	port := askInt("Proxy port?", 8788)
	addAlias := askYN("Add  alias qwen=qwencode-proxy  to your shell?", true)

	if err := pointSettingsAtProxy(upstream, port); err != nil {
		fatalf("setup: %v", err)
	}
	fmt.Printf("  ✓ backed up settings.json → %s\n", backupPath())

	enabled := hide
	cfg := Config{
		Upstream: upstream,
		Port:     port,
		Token:    newToken(),
		Rules: []Rule{
			{Type: ruleStripPair, Open: "<think>", Close: "</think>", Enabled: &enabled},
		},
	}
	if err := saveConfig(cfg); err != nil {
		fatalf("setup: %v", err)
	}
	fmt.Printf("  ✓ pointed qwen at the proxy · saved %s\n", configPath())

	if addAlias {
		addShellAlias()
	}
	fmt.Println("✓ setup complete — run  qwencode-proxy  (or  qwen  if you added the alias)")
}

// addShellAlias appends the alias for zsh/bash, else prints the line.
func addShellAlias() {
	const line = "alias qwen=qwencode-proxy"
	shell := filepath.Base(os.Getenv("SHELL"))
	home, _ := os.UserHomeDir()
	var rc string
	switch shell {
	case "zsh":
		rc = filepath.Join(home, ".zshrc")
	case "bash":
		rc = filepath.Join(home, ".bashrc")
	default:
		fmt.Printf("  ℹ add this to your %s config:  %s\n", shell, line)
		return
	}
	if b, err := os.ReadFile(rc); err == nil && strings.Contains(string(b), line) {
		fmt.Printf("  ✓ alias already in %s\n", rc)
		return
	}
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Printf("  ℹ add this to %s:  %s\n", rc, line)
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "\n%s\n", line)
	fmt.Printf("  ✓ added alias to %s (run: source %s)\n", rc, rc)
}
