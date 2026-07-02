package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "setup":
		runSetup()
	case "off":
		runOff()
	case "config":
		runConfigCmd(args[1:])
	case "-h", "--help", "help":
		usage()
	default:
		runLaunch(args)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Print(`qwencode-proxy — customize what flows between qwen-code and its model.

  qwencode-proxy [qwen args]   launch qwen behind the proxy (first run: setup)
  qwencode-proxy setup         (re)run setup: back up settings.json, point qwen at the proxy
  qwencode-proxy off           restore original settings.json baseUrl from backup
  qwencode-proxy config [-e]   show config (or open it in $EDITOR)
`)
}

func runOff() {
	if err := restoreSettings(); err != nil {
		fatalf("off: %v", err)
	}
	fmt.Println("✓ restored original settings.json — qwen now talks to the upstream directly")
}

func runConfigCmd(args []string) {
	if len(args) > 0 && (args[0] == "-e" || args[0] == "--edit") {
		ed := os.Getenv("EDITOR")
		if ed == "" {
			ed = "vi"
		}
		c := exec.Command(ed, configPath())
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		_ = c.Run()
		return
	}
	cfg, err := loadConfig()
	if err != nil {
		fatalf("no config at %s — run: qwencode-proxy setup", configPath())
	}
	fmt.Printf("config:   %s\n", configPath())
	fmt.Printf("upstream: %s   (real endpoint traffic is forwarded to)\n", cfg.Upstream)
	fmt.Printf("port:     %d   (local proxy; settings.json points here)\n", cfg.Port)
	fmt.Println("rules:")
	for _, r := range cfg.Rules {
		state := "on"
		if !ruleEnabled(r) {
			state = "off"
		}
		fmt.Printf("  - %-13s %s\n", r.Type, state)
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Printf("\n%s\n", b)
}

func runLaunch(qwenArgs []string) {
	cfg, err := loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			runSetup()
			cfg, err = loadConfig()
		}
		if err != nil {
			// config corrupt: recover enough to run transparently (fail-open)
			cfg = recoverConfig()
		}
	}
	if cfg.Port == 0 || cfg.Upstream == "" {
		fatalf("config unusable and unrecoverable — run: qwencode-proxy setup")
	}

	stop, err := ensureProxy(cfg)
	if err != nil {
		fatalf("proxy: %v", err)
	}
	defer stop()

	// self-heal: wire settings only after the proxy is confirmed listening
	if err := ensureWired(cfg); err != nil {
		stop()
		fatalf("wiring: %v", err)
	}

	c := exec.Command("qwen", qwenArgs...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		fatalf("launch qwen: %v", err)
	}
	// relay terminal signals to the child
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sig {
			if c.Process != nil {
				_ = c.Process.Signal(s)
			}
		}
	}()
	err = c.Wait()
	signal.Stop(sig)
	stop()
	os.Exit(exitCode(err))
}

// recoverConfig: minimal transparent config from settings+backup when config is corrupt.
func recoverConfig() Config {
	fmt.Fprintln(os.Stderr, "warning: config unreadable — running as a transparent proxy (no rules)")
	c := Config{Port: portFromSettings()}
	if b, err := os.ReadFile(backupPath()); err == nil {
		if u, e := upstreamFromRaw(b); e == nil {
			c.Upstream = u
		}
	}
	return c
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
