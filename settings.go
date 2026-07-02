package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func settingsPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".qwen", "settings.json")
}

var baseURLRe = regexp.MustCompile(`"baseUrl"\s*:\s*"([^"]+)"`)
var localPortRe = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)`)

// detectUpstream reads the real (non-localhost) baseUrl from qwen's settings.json.
func detectUpstream() (string, error) {
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		return "", fmt.Errorf("no ~/.qwen/settings.json — configure qwen first (run it once and set your provider)")
	}
	return upstreamFromRaw(raw)
}

// upstreamFromRaw finds the real upstream; prefers model.baseUrl, else unique non-localhost.
func upstreamFromRaw(raw []byte) (string, error) {
	var s struct {
		Model struct {
			BaseURL string `json:"baseUrl"`
		} `json:"model"`
	}
	if json.Unmarshal(raw, &s) == nil && isUpstream(s.Model.BaseURL) {
		return s.Model.BaseURL, nil
	}
	seen := map[string]bool{}
	for _, m := range baseURLRe.FindAllStringSubmatch(string(raw), -1) {
		if isUpstream(m[1]) {
			seen[m[1]] = true
		}
	}
	switch len(seen) {
	case 0:
		return "", fmt.Errorf("no upstream URL found in settings.json — configure qwen first")
	case 1:
		for u := range seen {
			return u, nil
		}
	}
	return "", fmt.Errorf("multiple upstream URLs in settings.json — set one provider and re-run setup")
}

func isUpstream(u string) bool {
	return strings.HasPrefix(u, "http") && !strings.Contains(u, "127.0.0.1") && !strings.Contains(u, "localhost")
}

func localURL(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

// pointSettingsAtProxy backs up, swaps upstream->local proxy URL, verifies.
func pointSettingsAtProxy(upstream string, port int) error {
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		return err
	}
	local := localURL(port)
	swapped := strings.ReplaceAll(string(raw), upstream, local)
	if !strings.Contains(swapped, local) {
		// upstream not present verbatim — nothing is written, backup left intact
		return fmt.Errorf("upstream %q not found verbatim in settings.json — aborting, nothing changed", upstream)
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	// raw is confirmed real-upstream content (contained `upstream`) — safe to back up
	if err := writeFileAtomic(backupPath(), raw, 0o600); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}
	if err := writeFileAtomic(settingsPath(), []byte(swapped), 0o600); err != nil {
		return err
	}
	check, err := os.ReadFile(settingsPath())
	if err != nil || !strings.Contains(string(check), local) {
		_ = restoreSettings()
		return fmt.Errorf("swap verification failed — restored backup")
	}
	return nil
}

// ensureWired points settings.json at our proxy if not already (idempotent).
func ensureWired(cfg Config) error {
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), localURL(cfg.Port)+`"`) {
		return nil // already wired (trailing quote anchors the match: :878 must not match :8788)
	}
	return pointSettingsAtProxy(cfg.Upstream, cfg.Port)
}

func restoreSettings() error {
	b, err := os.ReadFile(backupPath())
	if err != nil {
		return fmt.Errorf("no backup at %s", backupPath())
	}
	return writeFileAtomic(settingsPath(), b, 0o600)
}

// portFromSettings recovers the proxy port from settings.json's localhost baseUrl.
func portFromSettings() int {
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		return 0
	}
	if m := localPortRe.FindStringSubmatch(string(raw)); m != nil {
		p, _ := strconv.Atoi(m[1])
		return p
	}
	return 0
}
