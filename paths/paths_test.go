package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	// Test default (no XDG_CONFIG_HOME set)
	t.Run("default", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		got := ConfigHome()
		want := filepath.Join(home, ".config")
		if got != want {
			t.Errorf("ConfigHome() = %q, want %q", got, want)
		}
	})

	// Test with XDG_CONFIG_HOME set
	t.Run("custom", func(t *testing.T) {
		customConfig := "/custom/config"
		t.Setenv("XDG_CONFIG_HOME", customConfig)
		got := ConfigHome()
		if got != customConfig {
			t.Errorf("ConfigHome() with XDG_CONFIG_HOME = %q, want %q", got, customConfig)
		}
	})
}

func TestShelleyConfigDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	// Test default
	t.Run("default", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		got := ShelleyConfigDir()
		want := filepath.Join(home, ".config", "shelley")
		if got != want {
			t.Errorf("ShelleyConfigDir() = %q, want %q", got, want)
		}
	})

	// Test with XDG_CONFIG_HOME set
	t.Run("custom", func(t *testing.T) {
		customConfig := "/custom/config"
		t.Setenv("XDG_CONFIG_HOME", customConfig)
		got := ShelleyConfigDir()
		want := filepath.Join(customConfig, "shelley")
		if got != want {
			t.Errorf("ShelleyConfigDir() with XDG_CONFIG_HOME = %q, want %q", got, want)
		}
	})
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~/foo", filepath.Join(home, "foo")},
		{"~/foo/bar", filepath.Join(home, "foo", "bar")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~notahome", "~notahome"},
		{"", ""},
		{"~", "~"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ExpandPath(tt.input)
			if got != tt.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
