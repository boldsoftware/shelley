package lsp

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Manager manages the lifecycle of LSP servers, routing requests by file extension.
type Manager struct {
	workingDirFn func() string
	configs      []ServerConfig
	extToConfig  map[string]*ServerConfig // extension -> config

	mu      sync.Mutex
	servers map[string]*Server // config.Name -> running server
}

// NewManager creates a new LSP server manager.
func NewManager(workingDirFn func() string) *Manager {
	configs := DefaultServers()
	extToConfig := make(map[string]*ServerConfig)
	for i := range configs {
		for _, ext := range configs[i].Extensions {
			extToConfig[ext] = &configs[i]
		}
	}
	return &Manager{
		workingDirFn: workingDirFn,
		configs:      configs,
		extToConfig:  extToConfig,
		servers:      make(map[string]*Server),
	}
}

// GetServer returns a running LSP server for the given file path, starting one if needed.
func (m *Manager) GetServer(ctx context.Context, filePath string) (*Server, error) {
	ext := filepath.Ext(filePath)
	cfg, ok := m.extToConfig[ext]
	if !ok {
		return nil, fmt.Errorf("no LSP server configured for %s files", ext)
	}

	// Check if binary exists
	if _, err := exec.LookPath(cfg.Command); err != nil {
		return nil, fmt.Errorf("LSP server %q not found. %s", cfg.Command, cfg.InstallHint)
	}

	rootURI := m.rootURI()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for existing server
	if srv, ok := m.servers[cfg.Name]; ok {
		if srv.Alive() && srv.RootURI() == rootURI {
			return srv, nil
		}
		// Dead or stale root — shut down and restart
		slog.Info("lsp: restarting server", "server", cfg.Name, "reason", "dead or root changed")
		srv.Shutdown()
		delete(m.servers, cfg.Name)
	}

	// Start new server
	srv, err := NewServer(ctx, *cfg, rootURI)
	if err != nil {
		return nil, err
	}
	m.servers[cfg.Name] = srv
	slog.Info("lsp: started server", "server", cfg.Name, "rootURI", rootURI)
	return srv, nil
}

// ConfigForExt returns the server config for a file extension, or nil if none.
func (m *Manager) ConfigForExt(ext string) *ServerConfig {
	return m.extToConfig[ext]
}

// Close shuts down all running LSP servers.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, srv := range m.servers {
		slog.Info("lsp: shutting down server", "server", name)
		srv.Shutdown()
	}
	m.servers = make(map[string]*Server)
}

func (m *Manager) rootURI() string {
	wd := m.workingDirFn()
	root, err := findRepoRoot(wd)
	if err != nil {
		root = wd
	}
	return fileURI(root)
}

// findRepoRoot finds the git repository root from the given directory.
func findRepoRoot(wd string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = wd
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to find git repository root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
