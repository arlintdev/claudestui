package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds application settings.
type Config struct {
	PollInterval  int    `yaml:"poll_interval_ms"`  // polling interval in ms (default 2000)
	PreviewLines  int    `yaml:"preview_lines"`     // lines to capture for preview (default 30)
	DefaultDir    string `yaml:"default_dir"`       // default working directory
	ProfileDir    string `yaml:"profile_dir"`       // path to profile directory
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		PollInterval: 2000,
		PreviewLines: 30,
		DefaultDir:   home,
		ProfileDir:   filepath.Join(home, ".config", "claudes", "profiles"),
	}
}

// configDir returns the config directory path.
func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "claudes")
}

// Load reads the config from disk, or returns defaults.
func Load() Config {
	cfg := DefaultConfig()

	path := filepath.Join(configDir(), "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}

	_ = yaml.Unmarshal(data, &cfg)

	// Ensure defaults for zero values
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2000
	}
	if cfg.PreviewLines == 0 {
		cfg.PreviewLines = 30
	}
	if cfg.ProfileDir == "" {
		cfg.ProfileDir = DefaultConfig().ProfileDir
	}

	return cfg
}

// EnsureDir creates the config directory if it doesn't exist.
func EnsureDir() error {
	return os.MkdirAll(configDir(), 0o755)
}
