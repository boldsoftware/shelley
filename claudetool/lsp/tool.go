package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"shelley.exe.dev/llm"
)

const (
	codeIntelName        = "code_intelligence"
	codeIntelDescription = `Provides compiler-accurate code intelligence powered by Language Server Protocol (LSP).

Operations:
- definition: Go to the definition of a symbol at a given file position
- references: Find all references to a symbol at a given file position
- hover: Get type information and documentation for a symbol at a given file position
- symbols: Search for symbols (functions, types, variables) across the workspace

Use this for precise, semantic code navigation. For text-based search, use keyword_search instead.
Requires an LSP server installed for the file's language (e.g., gopls for Go, typescript-language-server for TypeScript).

Note: The first call for a language may be slow while the LSP server starts and indexes the workspace.
`
	codeIntelInputSchema = `{
  "type": "object",
  "required": ["operation"],
  "properties": {
    "operation": {
      "type": "string",
      "enum": ["definition", "references", "hover", "symbols"],
      "description": "The code intelligence operation to perform"
    },
    "file": {
      "type": "string",
      "description": "File path (absolute or relative to working directory). Required for definition, references, hover."
    },
    "line": {
      "type": "integer",
      "description": "Line number (1-based). Required for definition, references, hover."
    },
    "column": {
      "type": "integer",
      "description": "Column number (1-based). Required for definition, references, hover."
    },
    "query": {
      "type": "string",
      "description": "Symbol name to search for. Required for symbols operation."
    }
  }
}`
)

type codeIntelInput struct {
	Operation string `json:"operation"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Query     string `json:"query"`
}

// CodeIntelTool provides LSP-based code intelligence.
type CodeIntelTool struct {
	manager    *Manager
	workingDir func() string
}

// Tool returns the llm.Tool definition for code intelligence.
func (c *CodeIntelTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        codeIntelName,
		Description: codeIntelDescription,
		InputSchema: llm.MustSchema(codeIntelInputSchema),
		Run:         c.Run,
	}
}

// Run executes the code intelligence tool.
func (c *CodeIntelTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input codeIntelInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("failed to parse input: %w", err)
	}

	switch input.Operation {
	case "definition", "references", "hover":
		return c.runPositionOp(ctx, input)
	case "symbols":
		return c.runSymbols(ctx, input)
	default:
		return llm.ErrorfToolOut("unknown operation %q: must be one of definition, references, hover, symbols", input.Operation)
	}
}

func (c *CodeIntelTool) runPositionOp(ctx context.Context, input codeIntelInput) llm.ToolOut {
	if input.File == "" {
		return llm.ErrorfToolOut("file is required for %s operation", input.Operation)
	}
	if input.Line < 1 {
		return llm.ErrorfToolOut("line is required and must be >= 1 for %s operation", input.Operation)
	}
	if input.Column < 1 {
		return llm.ErrorfToolOut("column is required and must be >= 1 for %s operation", input.Operation)
	}

	// Resolve relative paths
	filePath := input.File
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(c.workingDir(), filePath)
	}
	filePath = filepath.Clean(filePath)

	// Get LSP server
	srv, err := c.manager.GetServer(ctx, filePath)
	if err != nil {
		return llm.ErrorfToolOut("%s", err)
	}

	// Open/refresh file in LSP
	if err := srv.OpenFile(ctx, filePath); err != nil {
		return llm.ErrorfToolOut("failed to open file in LSP: %s", err)
	}

	uri := fileURI(filePath)
	// Convert 1-based input to 0-based LSP positions
	pos := Position{
		Line:      input.Line - 1,
		Character: input.Column - 1,
	}

	wd := c.workingDir()

	switch input.Operation {
	case "definition":
		locations, err := srv.Definition(ctx, uri, pos)
		if err != nil {
			return llm.ErrorfToolOut("definition failed: %s", err)
		}
		return llm.ToolOut{LLMContent: llm.TextContent(formatDefinition(locations, wd))}

	case "references":
		locations, err := srv.References(ctx, uri, pos)
		if err != nil {
			return llm.ErrorfToolOut("references failed: %s", err)
		}
		return llm.ToolOut{LLMContent: llm.TextContent(formatReferences(locations, wd))}

	case "hover":
		hover, err := srv.HoverResult(ctx, uri, pos)
		if err != nil {
			return llm.ErrorfToolOut("hover failed: %s", err)
		}
		return llm.ToolOut{LLMContent: llm.TextContent(formatHover(hover))}
	}

	return llm.ErrorfToolOut("unreachable")
}

func (c *CodeIntelTool) runSymbols(ctx context.Context, input codeIntelInput) llm.ToolOut {
	if input.Query == "" {
		return llm.ErrorfToolOut("query is required for symbols operation")
	}

	// For symbols, we need any server — use the working dir to pick one.
	// Try to find an appropriate server by looking for common files.
	// Fall back to gopls if available.
	wd := c.workingDir()

	// Try to get a server. For workspace symbols, we'll try Go first then TS.
	var srv *Server
	var errs []string
	for _, ext := range []string{".go", ".ts"} {
		var err error
		srv, err = c.manager.GetServer(ctx, filepath.Join(wd, "dummy"+ext))
		if err == nil {
			break
		}
		errs = append(errs, err.Error())
	}
	if srv == nil {
		return llm.ErrorfToolOut("no LSP server available for symbols. Tried: %s", strings.Join(errs, "; "))
	}

	symbols, err := srv.WorkspaceSymbols(ctx, input.Query)
	if err != nil {
		return llm.ErrorfToolOut("workspace symbols failed: %s", err)
	}
	return llm.ToolOut{LLMContent: llm.TextContent(formatSymbols(symbols, wd))}
}
