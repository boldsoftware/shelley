package lsp

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// ServerConfig describes how to start an LSP server for a given language.
type ServerConfig struct {
	Name       string   // e.g., "gopls", "typescript-language-server"
	Command    string   // binary name
	Args       []string // command-line arguments
	Extensions []string // file extensions this server handles (e.g., ".go", ".ts")
	InstallHint string  // message shown if the binary is not found
}

// DefaultServers returns built-in server configurations.
func DefaultServers() []ServerConfig {
	return []ServerConfig{
		{
			Name:       "gopls",
			Command:    "gopls",
			Args:       []string{"serve"},
			Extensions: []string{".go"},
			InstallHint: "Install gopls: go install golang.org/x/tools/gopls@latest",
		},
		{
			Name:        "typescript-language-server",
			Command:     "typescript-language-server",
			Args:        []string{"--stdio"},
			Extensions:  []string{".ts", ".tsx", ".js", ".jsx"},
			InstallHint: "Install typescript-language-server: npm install -g typescript-language-server typescript",
		},
	}
}

// Server wraps a running LSP server process.
type Server struct {
	client  *Client
	config  ServerConfig
	rootURI string

	mu        sync.Mutex
	openFiles map[string]int // URI -> version
}

// NewServer starts an LSP server and initializes it with the given root URI.
func NewServer(ctx context.Context, config ServerConfig, rootURI string) (*Server, error) {
	cmd := exec.CommandContext(ctx, config.Command, config.Args...)
	cmd.Stderr = os.Stderr // let LSP server errors show
	client, err := NewClient(cmd)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", config.Name, err)
	}

	s := &Server{
		client:    client,
		config:    config,
		rootURI:   rootURI,
		openFiles: make(map[string]int),
	}

	if err := s.initialize(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("initialize %s: %w", config.Name, err)
	}

	return s, nil
}

func (s *Server) initialize(ctx context.Context) error {
	params := InitializeParams{
		ProcessID: os.Getpid(),
		RootURI:   s.rootURI,
		Capabilities: ClientCapabilities{
			TextDocument: &TextDocumentClientCapabilities{
				Definition: &DefinitionClientCapabilities{},
			},
		},
	}

	var result InitializeResult
	if err := s.client.Call(ctx, "initialize", params, &result); err != nil {
		return err
	}

	slog.Debug("lsp: initialized", "server", s.config.Name, "rootURI", s.rootURI)
	return s.client.Notify("initialized", struct{}{})
}

// OpenFile opens or refreshes a file in the LSP server by reading it from disk.
func (s *Server) OpenFile(ctx context.Context, filePath string) error {
	uri := fileURI(filePath)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file %s: %w", filePath, err)
	}

	lang := languageID(filePath)

	s.mu.Lock()
	version, isOpen := s.openFiles[uri]
	if isOpen {
		// File already open — send didChange with incremented version
		version++
		s.openFiles[uri] = version
		s.mu.Unlock()
		return s.client.Notify("textDocument/didChange", DidChangeTextDocumentParams{
			TextDocument: VersionedTextDocumentIdentifier{URI: uri, Version: version},
			ContentChanges: []TextDocumentContentChangeEvent{
				{Text: string(content)},
			},
		})
	}
	s.openFiles[uri] = 1
	s.mu.Unlock()

	return s.client.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:        uri,
			LanguageID: lang,
			Version:    1,
			Text:       string(content),
		},
	})
}

// CloseFile closes a file in the LSP server.
func (s *Server) CloseFile(uri string) {
	s.mu.Lock()
	delete(s.openFiles, uri)
	s.mu.Unlock()
	_ = s.client.Notify("textDocument/didClose", DidCloseTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
	})
}

// Definition returns the definition location(s) for the symbol at the given position.
func (s *Server) Definition(ctx context.Context, uri string, pos Position) ([]Location, error) {
	params := TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     pos,
	}
	// LSP spec: result is Location | Location[] | null
	// Try array first, then single location
	var locations []Location
	if err := s.client.Call(ctx, "textDocument/definition", params, &locations); err != nil {
		return nil, err
	}
	return locations, nil
}

// References returns all references to the symbol at the given position.
func (s *Server) References(ctx context.Context, uri string, pos Position) ([]Location, error) {
	params := ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     pos,
		Context:      ReferenceContext{IncludeDeclaration: true},
	}
	var locations []Location
	if err := s.client.Call(ctx, "textDocument/references", params, &locations); err != nil {
		return nil, err
	}
	return locations, nil
}

// HoverResult returns hover information for the symbol at the given position.
func (s *Server) HoverResult(ctx context.Context, uri string, pos Position) (*Hover, error) {
	params := TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     pos,
	}
	var hover Hover
	if err := s.client.Call(ctx, "textDocument/hover", params, &hover); err != nil {
		return nil, err
	}
	return &hover, nil
}

// WorkspaceSymbols searches for symbols matching the given query across the workspace.
func (s *Server) WorkspaceSymbols(ctx context.Context, query string) ([]SymbolInformation, error) {
	params := WorkspaceSymbolParams{Query: query}
	var symbols []SymbolInformation
	if err := s.client.Call(ctx, "workspace/symbol", params, &symbols); err != nil {
		return nil, err
	}
	return symbols, nil
}

// Alive returns true if the underlying LSP process is still running.
func (s *Server) Alive() bool {
	return s.client.Alive()
}

// RootURI returns the root URI this server was initialized with.
func (s *Server) RootURI() string {
	return s.rootURI
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown() {
	s.mu.Lock()
	for uri := range s.openFiles {
		_ = s.client.Notify("textDocument/didClose", DidCloseTextDocumentParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
		})
	}
	s.openFiles = make(map[string]int)
	s.mu.Unlock()
	_ = s.client.Close()
}

// fileURI converts an absolute file path to a file:// URI.
func fileURI(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	return u.String()
}

// filePathFromURI converts a file:// URI back to a file path.
func filePathFromURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	return u.Path
}

// languageID returns the LSP language ID for a file path based on its extension.
func languageID(path string) string {
	ext := filepath.Ext(path)
	switch ext {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".hpp", ".cc":
		return "cpp"
	default:
		return "plaintext"
	}
}
