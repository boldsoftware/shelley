package claudetool

import (
	"encoding/json"
	"slices"
	"testing"

	"shelley.exe.dev/llm"
)

// The three shapes of string-typed input models have sent, against the
// schemas of the tools that received them, decoded as those tools do.
func TestCoerceToolInputRealSchemas(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tool    *llm.Tool
		in      string
		coerced []string
		decode  func(json.RawMessage) error
	}{
		{
			name:    "patch array",
			tool:    (&PatchTool{}).Tool(),
			in:      `{"path":"/tmp/x.go","patches":"[{\"operation\":\"replace\",\"oldText\":\"\\t\\\"net/url\\\"\\n\",\"newText\":\"\\t\\\"net/url\\\"\\n\\t\\\"os\\\"\\n\"}]"}`,
			coerced: []string{"patches"},
			decode: func(in json.RawMessage) error {
				input, err := (&PatchTool{}).patchParse(in)
				if err == nil && (input.Patches[0].OldText != "\t\"net/url\"\n" || input.Patches[0].NewText != "\t\"net/url\"\n\t\"os\"\n") {
					t.Errorf("patches decoded as %+v", input.Patches)
				}
				return err
			},
		},
		{
			name:    "message_parent boolean",
			tool:    (&MessageParentTool{}).Tool(),
			in:      `{"text":"Done: b8d12807 (see my final reply).","end_turn":"true"}`,
			coerced: []string{"end_turn"},
			decode: func(in json.RawMessage) error {
				var req struct {
					EndTurn *bool `json:"end_turn"`
				}
				err := json.Unmarshal(in, &req)
				if err == nil && (req.EndTurn == nil || !*req.EndTurn) {
					t.Errorf("end_turn decoded as %v", req.EndTurn)
				}
				return err
			},
		},
		{
			name:    "bash boolean",
			tool:    (&BashTool{}).Tool(),
			in:      `{"command":"make build","background":"true"}`,
			coerced: []string{"background"},
			decode: func(in json.RawMessage) error {
				var req bashInput
				err := json.Unmarshal(in, &req)
				if err == nil && !req.Background {
					t.Error("background decoded as false")
				}
				return err
			},
		},
		{
			// JSON-looking text in a string member is the command itself.
			name: "bash command",
			tool: (&BashTool{}).Tool(),
			in:   `{"command":"[1,2]"}`,
			decode: func(in json.RawMessage) error {
				var req bashInput
				err := json.Unmarshal(in, &req)
				if err == nil && req.Command != "[1,2]" {
					t.Errorf("command decoded as %q", req.Command)
				}
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := json.RawMessage(tc.in)
			if tc.coerced != nil && tc.decode(in) == nil {
				t.Fatal("tool accepted the uncoerced input; the test no longer exercises coercion")
			}
			got, coerced := llm.CoerceToolInput(in, tc.tool.InputSchema)
			if !slices.Equal(coerced, tc.coerced) {
				t.Fatalf("coerced %v, want %v", coerced, tc.coerced)
			}
			if tc.coerced == nil && &got[0] != &in[0] {
				t.Fatal("unchanged input was copied")
			}
			if err := tc.decode(got); err != nil {
				t.Fatalf("decode %s: %v", got, err)
			}
		})
	}
}
