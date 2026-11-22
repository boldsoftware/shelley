package claudetool

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"shelley.exe.dev/llm"
)

func TestToolRegistrySourcesExist(t *testing.T) {
	for _, tool := range ToolRegistry {
		if tool.SourcePath == "" {
			t.Fatalf("tool %q has no source path", tool.Name)
		}
		if _, err := os.Stat(filepath.Join("..", tool.SourcePath)); err != nil {
			t.Fatalf("tool %q source %q: %v", tool.Name, tool.SourcePath, err)
		}
		found, ok := ToolInfoByName(tool.Name)
		if !ok || found.SourcePath != tool.SourcePath {
			t.Fatalf("ToolInfoByName(%q) = %+v, %v", tool.Name, found, ok)
		}
	}
	patch, ok := ToolInfoByName("apply_patch")
	if !ok || patch.SourcePath != "claudetool/patch.go" {
		t.Fatalf("apply_patch source = %+v, %v", patch, ok)
	}
}

func TestIsToolEnabled(t *testing.T) {
	cases := []struct {
		name        string
		tool        string
		overrides   map[string]string
		disableAll  bool
		wantEnabled bool
	}{
		{"default on", "bash", nil, false, true},
		{"override off", "bash", map[string]string{"bash": "off"}, false, false},
		{"override on trumps disable-all", "bash", map[string]string{"bash": "on"}, true, true},
		{"disable all turns default on to off", "bash", nil, true, false},
		{"apply_patch uses patch default", "apply_patch", nil, false, true},
		{"apply_patch uses patch off", "apply_patch", map[string]string{"patch": "off"}, false, false},
		{"apply_patch uses patch on", "apply_patch", map[string]string{"patch": "on"}, true, true},
		{"apply_patch follows disable all", "apply_patch", nil, true, false},
		{"compact_in_place default on", "compact_in_place", nil, false, true},
		{"output_html default on", "output_html", nil, false, true},
		{"output_iframe default off", "output_iframe", nil, false, false},
		{"read_code default off", "read_code", nil, false, false},
		{"read_code opt in", "read_code", map[string]string{"read_code": "on"}, false, true},
		{"read_code opt in trumps disable-all", "read_code", map[string]string{"read_code": "on"}, true, true},
		{"read_code explicit off", "read_code", map[string]string{"read_code": "off"}, false, false},
		{"unknown tool permissive", "new_experimental_tool", nil, false, true},
		{"unknown tool under disable-all is off", "new_experimental_tool", nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsToolEnabled(tc.tool, tc.overrides, tc.disableAll)
			if got != tc.wantEnabled {
				t.Fatalf("IsToolEnabled(%q) = %v, want %v", tc.tool, got, tc.wantEnabled)
			}
		})
	}
}

func TestNewToolSetReadCodeOptIn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		overrides map[string]string
		vm        VM
		want      bool
	}{
		{name: "default"},
		{name: "enabled", overrides: map[string]string{"read_code": "on"}, want: true},
		{name: "disabled", overrides: map[string]string{"read_code": "off"}},
		{name: "remote VM", overrides: map[string]string{"read_code": "on"}, vm: localVM{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := NewToolSet(t.Context(), ToolSetConfig{ToolOverrides: tc.overrides, VM: tc.vm})
			defer ts.Cleanup()
			found := false
			for _, tool := range ts.Tools() {
				found = found || tool.Name == readCodeName
			}
			if found != tc.want {
				t.Fatalf("read_code present = %v, want %v", found, tc.want)
			}
		})
	}
}

func TestFilterTools(t *testing.T) {
	tools := []*llm.Tool{
		{Name: "bash"},
		{Name: "patch"},
		{Name: "apply_patch"},
		{Name: "browser"},
	}
	filtered := FilterTools(tools, map[string]string{"patch": "off"}, false)
	names := make([]string, 0, len(filtered))
	for _, tt := range filtered {
		names = append(names, tt.Name)
	}
	sort.Strings(names)
	want := []string{"bash", "browser"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range names {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}

func TestNewToolSetRespectsOverrides(t *testing.T) {
	ctx := t.Context()
	ts := NewToolSet(ctx, ToolSetConfig{
		ToolOverrides: map[string]string{"bash": "off"},
	})
	defer ts.Cleanup()
	for _, tool := range ts.Tools() {
		if tool.Name == "chapter" {
			t.Fatal("chapter indexing must not be a conversation tool")
		}
		if tool.Name == "bash" {
			t.Fatalf("bash should have been filtered out")
		}
	}
}

func TestNewToolSetDisableAllTools(t *testing.T) {
	ctx := t.Context()
	ts := NewToolSet(ctx, ToolSetConfig{
		DisableAllTools: true,
		ToolOverrides:   map[string]string{"bash": "on"},
	})
	defer ts.Cleanup()
	var names []string
	for _, tool := range ts.Tools() {
		names = append(names, tool.Name)
	}
	if len(names) != 1 || names[0] != "bash" {
		t.Fatalf("expected only bash, got %v", names)
	}
}
