package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// Rule is one transform. Unknown fields for a given type are simply unused.
type Rule struct {
	Type     string         `json:"type"`
	Enabled  *bool          `json:"enabled,omitempty"`  // absent = enabled
	Open     string         `json:"open,omitempty"`     // strip-pair
	Close    string         `json:"close,omitempty"`    // strip-pair
	Find     string         `json:"find,omitempty"`     // replace
	Replace  string         `json:"replace,omitempty"`  // replace
	Text     string         `json:"text,omitempty"`     // inject-system
	Position string         `json:"position,omitempty"` // inject-system: prepend|append
	Params   map[string]any `json:"params,omitempty"`   // set-param
}

type Config struct {
	Upstream string `json:"upstream"` // real endpoint, saved from settings.json at setup
	Port     int    `json:"port"`
	Token    string `json:"token,omitempty"` // per-instance secret proving a listener is ours
	Rules    []Rule `json:"rules"`
}

// newToken: random per-instance secret proving the listener is ours; "" on failure.
func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func configDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "qwencode-proxy")
}
func configPath() string { return filepath.Join(configDir(), "config.json") }
func backupPath() string { return filepath.Join(configDir(), "settings.json.bak") }

func loadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath())
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

func saveConfig(c Config) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(configPath(), b, 0o600)
}

// writeFileAtomic: temp-file + rename, so a crash can't leave a corrupt file; enforces perm.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
