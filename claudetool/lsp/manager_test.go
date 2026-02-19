package lsp

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestManagerConfigForExt(t *testing.T) {
	m := NewManager(func() string { return "/tmp" })

	tests := []struct {
		ext      string
		wantName string
		wantNil  bool
	}{
		{".go", "gopls", false},
		{".ts", "typescript-language-server", false},
		{".tsx", "typescript-language-server", false},
		{".js", "typescript-language-server", false},
		{".jsx", "typescript-language-server", false},
		{".py", "", true},
		{".rs", "", true},
		{".txt", "", true},
	}
	for _, tt := range tests {
		cfg := m.ConfigForExt(tt.ext)
		if tt.wantNil {
			if cfg != nil {
				t.Errorf("ConfigForExt(%q) = %v, want nil", tt.ext, cfg)
			}
			continue
		}
		if cfg == nil {
			t.Errorf("ConfigForExt(%q) = nil, want %q", tt.ext, tt.wantName)
			continue
		}
		if cfg.Name != tt.wantName {
			t.Errorf("ConfigForExt(%q).Name = %q, want %q", tt.ext, cfg.Name, tt.wantName)
		}
	}
}

func TestManagerGetServerMissingBinary(t *testing.T) {
	m := NewManager(func() string { return t.TempDir() })

	// Override configs to use a nonexistent binary
	m.configs = []ServerConfig{
		{
			Name:        "fake-lsp",
			Command:     "definitely-not-a-real-binary-12345",
			Args:        []string{},
			Extensions:  []string{".fake"},
			InstallHint: "Install fake-lsp: go install fake-lsp@latest",
		},
	}
	m.extToConfig = map[string]*ServerConfig{
		".fake": &m.configs[0],
	}

	ctx := context.Background()
	_, err := m.GetServer(ctx, "/tmp/test.fake")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	// Error should contain the install hint
	if got := err.Error(); !contains(got, "Install fake-lsp") {
		t.Errorf("error %q should contain install hint", got)
	}
}

func TestManagerGetServerNoConfig(t *testing.T) {
	m := NewManager(func() string { return t.TempDir() })

	ctx := context.Background()
	_, err := m.GetServer(ctx, "/tmp/test.py")
	if err == nil {
		t.Fatal("expected error for unconfigured extension")
	}
}

func TestManagerGetServerReusesServer(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}

	dir := t.TempDir()
	m := NewManager(func() string { return dir })
	defer m.Close()

	ctx := context.Background()
	goFile := filepath.Join(dir, "main.go")

	srv1, err := m.GetServer(ctx, goFile)
	if err != nil {
		t.Fatalf("first GetServer: %v", err)
	}

	srv2, err := m.GetServer(ctx, goFile)
	if err != nil {
		t.Fatalf("second GetServer: %v", err)
	}

	if srv1 != srv2 {
		t.Error("expected same server instance for repeat calls")
	}
}

func TestManagerClose(t *testing.T) {
	m := NewManager(func() string { return t.TempDir() })
	// Close with no servers should not panic
	m.Close()
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
