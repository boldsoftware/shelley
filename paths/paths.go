// Package paths provides utilities for resolving configuration paths.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// ConfigHome returns the XDG config directory.
// It uses XDG_CONFIG_HOME if set, otherwise falls back to ~/.config.
func ConfigHome() string {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return configHome
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config")
	}
	return ""
}

// ShelleyConfigDir returns the shelley-specific config directory.
// This is $XDG_CONFIG_HOME/shelley or ~/.config/shelley.
func ShelleyConfigDir() string {
	if configHome := ConfigHome(); configHome != "" {
		return filepath.Join(configHome, "shelley")
	}
	return ""
}

// ExpandPath expands ~ to the user's home directory.
func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
