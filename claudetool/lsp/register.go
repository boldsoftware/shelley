package lsp

import "shelley.exe.dev/llm"

// RegisterLSPTools creates the LSP code intelligence tools and returns them with a cleanup function.
// The cleanup function shuts down all running LSP servers.
func RegisterLSPTools(workingDirFn func() string) ([]*llm.Tool, func()) {
	manager := NewManager(workingDirFn)
	tool := &CodeIntelTool{
		manager:    manager,
		workingDir: workingDirFn,
	}
	return []*llm.Tool{tool.Tool()}, manager.Close
}
