package bashkit

import (
	"reflect"
	"testing"
)

func TestExtractCommands(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple command",
			input:    "ls -la",
			expected: []string{"ls"},
		},
		{
			name:     "command with pipe",
			input:    "ls -la | grep test",
			expected: []string{"ls", "grep"},
		},
		{
			name:     "command with logical and (builtin filtered)",
			input:    "mkdir test && cd test",
			expected: []string{"mkdir"}, // cd is builtin, filtered out
		},
		{
			name:     "if statement with commands (builtin filtered)",
			input:    "if [ -f file.txt ]; then cat file.txt; fi",
			expected: []string{"cat"}, // [ is builtin, filtered out
		},
		{
			name:     "variable assignment with command (builtin filtered)",
			input:    "FOO=bar echo $FOO",
			expected: []string{}, // echo is builtin, filtered out
		},
		{
			name:     "script path filtered out (builtin also filtered)",
			input:    "./script.sh && echo done",
			expected: []string{}, // echo is builtin, filtered out
		},
		{
			name:     "multiline script (builtin filtered)",
			input:    "python3 -c 'print(\"hello\")'\necho 'done'",
			expected: []string{"python3"}, // echo is builtin, filtered out
		},
		{
			name:     "complex command chain (builtin filtered)",
			input:    "curl -s https://api.github.com | jq '.name' && echo 'done'",
			expected: []string{"curl", "jq"}, // echo is builtin, filtered out
		},
		{
			name:     "builtins filtered out",
			input:    "echo 'test' && true && ls",
			expected: []string{"ls"},
		},
		{
			name:     "empty command",
			input:    "",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractCommands(tt.input)
			if err != nil {
				t.Fatalf("ExtractCommands() error = %v", err)
			}
			// Handle empty slice comparison
			if len(result) == 0 && len(tt.expected) == 0 {
				return // Both are empty, test passes
			}
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ExtractCommands() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractCommandsErrorHandling(t *testing.T) {
	// Test with syntactically invalid bash
	invalidBash := "if [ incomplete"
	_, err := ExtractCommands(invalidBash)
	if err == nil {
		t.Error("ExtractCommands() should return error for invalid bash syntax")
	}
}

func TestExtractCommandsPathFiltering(t *testing.T) {
	// Test that commands with paths are properly filtered out during extraction
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "relative script path filtered (builtin also filtered)",
			input:    "./my-script.sh && echo 'done'",
			expected: []string{}, // echo is builtin, filtered out
		},
		{
			name:     "absolute path filtered",
			input:    "/usr/bin/custom-tool --help",
			expected: []string{},
		},
		{
			name:     "parent directory script filtered",
			input:    "../scripts/build.sh",
			expected: []string{},
		},
		{
			name:     "home directory path filtered",
			input:    "~/.local/bin/tool",
			expected: []string{},
		},
		{
			name:     "simple commands without paths included",
			input:    "curl https://example.com | jq '.name'",
			expected: []string{"curl", "jq"},
		},
		{
			name:     "mixed paths and simple commands",
			input:    "./setup.sh && python3 -c 'print(\"hello\")' && /bin/ls",
			expected: []string{"python3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractCommands(tt.input)
			if err != nil {
				t.Fatalf("ExtractCommands() error = %v", err)
			}
			// Handle empty slice comparison
			if len(result) == 0 && len(tt.expected) == 0 {
				return // Both are empty, test passes
			}
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ExtractCommands() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractCommandsEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty command name",
			input:    "",
			expected: []string{},
		},
		{
			name:     "duplicate commands deduplication",
			input:    "ls -la && ls -la",
			expected: []string{"ls"},
		},
		{
			name:     "multiple duplicates with different order",
			input:    "git status && ls -la && git add . && ls -la",
			expected: []string{"git", "ls"},
		},
		{
			name:     "variable assignment with non-builtin command",
			input:    "TEST=value mytool",
			expected: []string{"mytool"},
		},
		{
			name:     "command with slash in name filtered out",
			input:    "path/to/command --help",
			expected: []string{},
		},
		{
			name:     "command with empty name",
			input:    "\"\" arg", // Command with empty string name
			expected: []string{},
		},
		{
			name:     "builtin command filtered out",
			input:    "echo hello",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractCommands(tt.input)
			if err != nil {
				t.Fatalf("ExtractCommands() error = %v", err)
			}
			if len(result) == 0 && len(tt.expected) == 0 {
				return
			}
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ExtractCommands() = %v, want %v", result, tt.expected)
			}
		})
	}
}
