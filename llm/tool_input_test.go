package llm

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestCanonicalToolInput(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		repeated []string
	}{
		{in: `{"b":1, "a":[{"x":1}]}`, want: `{"b":1, "a":[{"x":1}]}`},
		{in: `{"ssh_target":"\">\n<parameter name=\"command\">ls","ssh_target":""}`, want: `{"ssh_target":""}`, repeated: []string{"ssh_target"}},
		{in: `{"a":{"x":1,"x":2},"b":[{"y":1,"y":1.50}],"x":3}`, want: `{"a":{"x":2},"b":[{"y":1.50}],"x":3}`, repeated: []string{"x", "y"}},
		{in: `not json`, want: `not json`},
	} {
		got, repeated := CanonicalToolInput(json.RawMessage(tc.in))
		if string(got) != tc.want || !slices.Equal(repeated, tc.repeated) {
			t.Errorf("CanonicalToolInput(%s) = %s %v, want %s %v", tc.in, got, repeated, tc.want, tc.repeated)
		}
	}
}

func TestCoerceToolInput(t *testing.T) {
	schema := MustSchema(`{"type":"object","properties":{
		"text":{"type":"string"},
		"end_turn":{"type":"boolean"},
		"timeout_seconds":{"type":"integer"},
		"ratio":{"type":"number"},
		"patches":{"type":"array","items":{"type":"object"}},
		"options":{"type":"object"},
		"note":{"type":["string","null"]},
		"count":{"type":["integer","null"]},
		"ref":{"$ref":"#/$defs/x"}
	}}`)
	for _, tc := range []struct {
		in, want string
		coerced  []string
	}{
		{in: `{"text":"hi", "end_turn":"true"}`, want: `{"text":"hi", "end_turn":true}`, coerced: []string{"end_turn"}},
		{in: `{"end_turn":"false","text":"hi"}`, want: `{"end_turn":false,"text":"hi"}`, coerced: []string{"end_turn"}},
		{in: `{"end_turn": " true "}`, want: `{"end_turn": true}`, coerced: []string{"end_turn"}},
		{in: `{"timeout_seconds":"30"}`, want: `{"timeout_seconds":30}`, coerced: []string{"timeout_seconds"}},
		{in: `{"ratio":"30"}`, want: `{"ratio":30}`, coerced: []string{"ratio"}},
		{in: `{"ratio":"1.5e3"}`, want: `{"ratio":1.5e3}`, coerced: []string{"ratio"}},
		{in: `{"count":"7"}`, want: `{"count":7}`, coerced: []string{"count"}},
		{in: `{"patches":"[{\"operation\":\"append_eof\",\"newText\":\"a\\n\"}]","path":"f"}`, want: `{"patches":[{"operation":"append_eof","newText":"a\n"}],"path":"f"}`, coerced: []string{"patches"}},
		{in: `{"options":"{\"a\":1}","end_turn":"true"}`, want: `{"options":{"a":1},"end_turn":true}`, coerced: []string{"options", "end_turn"}},
		// Strings that are not JSON of the declared type stay strings.
		{in: `{"end_turn":"yes"}`, want: `{"end_turn":"yes"}`},
		{in: `{"end_turn":"[1]"}`, want: `{"end_turn":"[1]"}`},
		{in: `{"end_turn":""}`, want: `{"end_turn":""}`},
		{in: `{"timeout_seconds":"1.5"}`, want: `{"timeout_seconds":"1.5"}`},
		{in: `{"timeout_seconds":"null"}`, want: `{"timeout_seconds":"null"}`},
		{in: `{"patches":"[{\"operation\":"}`, want: `{"patches":"[{\"operation\":"}`},
		{in: `{"patches":"[1] x"}`, want: `{"patches":"[1] x"}`},
		// Members typed to hold a string are never touched.
		{in: `{"text":"[1,2]","note":"true","ref":"1","unknown":"{}"}`, want: `{"text":"[1,2]","note":"true","ref":"1","unknown":"{}"}`},
		// Well-typed and non-object input is unchanged.
		{in: `{"end_turn":true,"patches":["true"]}`, want: `{"end_turn":true,"patches":["true"]}`},
		{in: `{"end_turn":"true","end_turn":"false"}`, want: `{"end_turn":true,"end_turn":false}`, coerced: []string{"end_turn", "end_turn"}},
		{in: `["true"]`, want: `["true"]`},
		{in: `"true"`, want: `"true"`},
		{in: `{"end_turn":`, want: `{"end_turn":`},
		{in: `not json`, want: `not json`},
	} {
		in := json.RawMessage(tc.in)
		got, coerced := CoerceToolInput(in, schema)
		if string(got) != tc.want || !slices.Equal(coerced, tc.coerced) {
			t.Errorf("CoerceToolInput(%s) = %s %v, want %s %v", tc.in, got, coerced, tc.want, tc.coerced)
		}
		if tc.coerced == nil && &got[0] != &in[0] {
			t.Errorf("CoerceToolInput(%s) copied unchanged input", tc.in)
		}
	}
	for _, schema := range []json.RawMessage{nil, EmptySchema(), json.RawMessage(`{"type":"object"}`)} {
		in := json.RawMessage(`{"end_turn":"true"}`)
		if got, coerced := CoerceToolInput(in, schema); &got[0] != &in[0] || coerced != nil {
			t.Errorf("CoerceToolInput(%s, %s) = %s %v", in, schema, got, coerced)
		}
	}
}

func TestReplayToolInput(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"empty", "", `{}`},
		{"whitespace", " \n\t", `{}`},
		{"null", " \nnull\t", `{}`},
		{"quoted object", `"{\"action\":\"eval\"}"`, `{}`},
		{"quoted truncated object", `"{\"action\":\"eval\""`, `{}`},
		{"string", `"hello"`, `{}`},
		{"number", `42`, `{}`},
		{"boolean", `true`, `{}`},
		{"array", `[{"action":"eval"}]`, `{}`},
		{"truncated", `{"action":`, `{}`},
		{"trailing garbage", `{"a":1} trailing`, `{}`},
		{"multiple objects", `{} {}`, `{}`},
		{"empty object", `{}`, `{}`},
		{"valid object bytes", " \n{ \"b\":1.50, \"a\":9007199254740993 }\t", " \n{ \"b\":1.50, \"a\":9007199254740993 }\t"},
		{"invalid field type", `{"action":42}`, `{"action":42}`},
		{"repeated names", `{"a":{"x":1,"x":2},"b":[{"y":1,"y":1.50}]}`, `{"a":{"x":2},"b":[{"y":1.50}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := json.RawMessage(tc.input)
			for range 2 {
				if got := string(ReplayToolInput(input)); got != tc.want {
					t.Fatalf("replay = %q, want %q", got, tc.want)
				}
				if string(input) != tc.input {
					t.Fatalf("original input changed: %q", input)
				}
			}
		})
	}
	if got := string(ReplayToolInput(nil)); got != `{}` {
		t.Fatalf("nil replay = %q", got)
	}
}
