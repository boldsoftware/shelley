package lsp

// LSP protocol types — minimal subset needed for code intelligence operations.

// Position in a text document (0-based line and character).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range in a text document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location represents a location inside a resource.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// TextDocumentIdentifier identifies a text document by its URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// TextDocumentItem is an item to transfer a text document from client to server.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// TextDocumentPositionParams is a parameter literal used in requests that require a position in a text document.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// ReferenceContext controls whether declarations should be included in references results.
type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// ReferenceParams extends TextDocumentPositionParams with reference context.
type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      ReferenceContext        `json:"context"`
}

// WorkspaceSymbolParams is the params for a workspace/symbol request.
type WorkspaceSymbolParams struct {
	Query string `json:"query"`
}

// SymbolKind represents the kind of a symbol.
type SymbolKind int

const (
	SymbolKindFile          SymbolKind = 1
	SymbolKindModule        SymbolKind = 2
	SymbolKindNamespace     SymbolKind = 3
	SymbolKindPackage       SymbolKind = 4
	SymbolKindClass         SymbolKind = 5
	SymbolKindMethod        SymbolKind = 6
	SymbolKindProperty      SymbolKind = 7
	SymbolKindField         SymbolKind = 8
	SymbolKindConstructor   SymbolKind = 9
	SymbolKindEnum          SymbolKind = 10
	SymbolKindInterface     SymbolKind = 11
	SymbolKindFunction      SymbolKind = 12
	SymbolKindVariable      SymbolKind = 13
	SymbolKindConstant      SymbolKind = 14
	SymbolKindString        SymbolKind = 15
	SymbolKindNumber        SymbolKind = 16
	SymbolKindBoolean       SymbolKind = 17
	SymbolKindArray         SymbolKind = 18
	SymbolKindObject        SymbolKind = 19
	SymbolKindKey           SymbolKind = 20
	SymbolKindNull          SymbolKind = 21
	SymbolKindEnumMember    SymbolKind = 22
	SymbolKindStruct        SymbolKind = 23
	SymbolKindEvent         SymbolKind = 24
	SymbolKindOperator      SymbolKind = 25
	SymbolKindTypeParameter SymbolKind = 26
)

// SymbolKindName returns a human-readable name for a SymbolKind.
func SymbolKindName(k SymbolKind) string {
	names := map[SymbolKind]string{
		SymbolKindFile:          "File",
		SymbolKindModule:        "Module",
		SymbolKindNamespace:     "Namespace",
		SymbolKindPackage:       "Package",
		SymbolKindClass:         "Class",
		SymbolKindMethod:        "Method",
		SymbolKindProperty:      "Property",
		SymbolKindField:         "Field",
		SymbolKindConstructor:   "Constructor",
		SymbolKindEnum:          "Enum",
		SymbolKindInterface:     "Interface",
		SymbolKindFunction:      "Function",
		SymbolKindVariable:      "Variable",
		SymbolKindConstant:      "Constant",
		SymbolKindString:        "String",
		SymbolKindNumber:        "Number",
		SymbolKindBoolean:       "Boolean",
		SymbolKindArray:         "Array",
		SymbolKindObject:        "Object",
		SymbolKindKey:           "Key",
		SymbolKindNull:          "Null",
		SymbolKindEnumMember:    "EnumMember",
		SymbolKindStruct:        "Struct",
		SymbolKindEvent:         "Event",
		SymbolKindOperator:      "Operator",
		SymbolKindTypeParameter: "TypeParameter",
	}
	if name, ok := names[k]; ok {
		return name
	}
	return "Unknown"
}

// SymbolInformation represents information about a programming construct.
type SymbolInformation struct {
	Name          string     `json:"name"`
	Kind          SymbolKind `json:"kind"`
	Location      Location   `json:"location"`
	ContainerName string     `json:"containerName,omitempty"`
}

// Hover is the result of a hover request.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// MarkupContent represents a string value with a specific content type.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// InitializeParams is sent as the first request from client to server.
type InitializeParams struct {
	ProcessID    int                `json:"processId"`
	RootURI      string             `json:"rootUri"`
	Capabilities ClientCapabilities `json:"capabilities"`
}

// ClientCapabilities define capabilities the editor / tool provides.
type ClientCapabilities struct {
	TextDocument *TextDocumentClientCapabilities `json:"textDocument,omitempty"`
}

// TextDocumentClientCapabilities define capabilities the editor / tool provides on text documents.
type TextDocumentClientCapabilities struct {
	Definition *DefinitionClientCapabilities `json:"definition,omitempty"`
}

// DefinitionClientCapabilities indicates whether definition supports dynamic registration.
type DefinitionClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// InitializeResult is the result returned from the initialize request.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
}

// ServerCapabilities define capabilities the language server provides.
type ServerCapabilities struct {
	TextDocumentSync           any  `json:"textDocumentSync,omitempty"`
	DefinitionProvider         bool `json:"definitionProvider,omitempty"`
	ReferencesProvider         bool `json:"referencesProvider,omitempty"`
	HoverProvider              bool `json:"hoverProvider,omitempty"`
	WorkspaceSymbolProvider    bool `json:"workspaceSymbolProvider,omitempty"`
	DocumentSymbolProvider     bool `json:"documentSymbolProvider,omitempty"`
	CompletionProvider         any  `json:"completionProvider,omitempty"`
	SignatureHelpProvider      any  `json:"signatureHelpProvider,omitempty"`
	DocumentFormattingProvider bool `json:"documentFormattingProvider,omitempty"`
}

// DidOpenTextDocumentParams is sent when a text document is opened.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// DidCloseTextDocumentParams is sent when a text document is closed.
type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// TextDocumentContentChangeEvent describes an event describing a change to a text document.
type TextDocumentContentChangeEvent struct {
	Text string `json:"text"`
}

// VersionedTextDocumentIdentifier is a text document identifier with a version.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// DidChangeTextDocumentParams is sent when the content of a text document changes.
type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}
