package llm

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// ReplayToolInput returns object-shaped arguments for provider history.
// Input that is not one valid JSON object replays as {}; the original
// input kept for storage and execution is never changed.
func ReplayToolInput(input json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(input) {
		return json.RawMessage(`{}`)
	}
	input, _ = CanonicalToolInput(input)
	return input
}

// CanonicalToolInput returns a tool call's input with every object member
// name appearing once, keeping the last value as encoding/json does, and
// the names that were repeated. Models occasionally garble a call into
// input such as {"a":"…","a":""}; encoding/json accepts it, but strict
// provider parsers reject the whole request, so such input must never be
// sent back. Input without repeated names is returned unchanged, keeping
// its exact bytes (and the provider's prompt cache).
func CanonicalToolInput(input json.RawMessage) (json.RawMessage, []string) {
	repeated := repeatedNames(input)
	if len(repeated) == 0 {
		return input, nil
	}
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return input, nil
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return input, nil
	}
	return canonical, repeated
}

// CoerceToolInput returns a tool call's input with each top-level member
// holding a JSON string whose contents are JSON of the type the tool's
// input schema declares for it (boolean, integer, number, array or object)
// replaced by those contents, and the names of the members replaced.
// Models occasionally send {"end_turn":"true"} or {"patches":"[{…}]"},
// which a tool's decoder rejects. A member whose schema type admits a
// string, or whose string is not JSON of the declared type, is left for
// the tool's decoder to judge; so is input that is not an object. All
// other bytes are kept, and input needing no change is returned unchanged,
// keeping its exact bytes (and the provider's prompt cache).
func CoerceToolInput(input json.RawMessage, schema json.RawMessage) (json.RawMessage, []string) {
	var s struct {
		Properties map[string]struct {
			Type json.RawMessage `json:"type"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil || len(s.Properties) == 0 {
		return input, nil
	}
	d := json.NewDecoder(bytes.NewReader(input))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return input, nil
	}
	var out []byte
	var coerced []string
	last := 0
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return input, nil
		}
		name := t.(string)
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return input, nil
		}
		end := int(d.InputOffset()) // raw is exactly input[end-len(raw):end]
		if raw[0] != '"' {
			continue
		}
		prop, ok := s.Properties[name]
		if !ok {
			continue
		}
		var contents string
		json.Unmarshal(raw, &contents) // raw is a valid JSON string
		value := []byte(strings.TrimSpace(contents))
		if schemaTypeAccepts(prop.Type, "string") || !json.Valid(value) || !schemaTypeAccepts(prop.Type, jsonType(value)) {
			continue
		}
		out = append(out, input[last:end-len(raw)]...)
		out = append(out, value...)
		last = end
		coerced = append(coerced, name)
	}
	if len(coerced) == 0 {
		return input, nil
	}
	return append(out, input[last:]...), coerced
}

// schemaTypeAccepts reports whether a JSON schema "type" member, a type
// name or a list of them, names typ. "number" accepts "integer".
func schemaTypeAccepts(schemaType json.RawMessage, typ string) bool {
	var types []string
	if json.Unmarshal(schemaType, &types) != nil {
		var one string
		if json.Unmarshal(schemaType, &one) != nil {
			return false
		}
		types = []string{one}
	}
	return slices.Contains(types, typ) || typ == "integer" && slices.Contains(types, "number")
}

// jsonType names the JSON schema type of one valid JSON value.
func jsonType(value []byte) string {
	switch value[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	if bytes.ContainsAny(value, ".eE") {
		return "number"
	}
	return "integer"
}

// repeatedNames lists the member names repeated within any one object of
// input, or nil if input is not valid JSON.
func repeatedNames(input json.RawMessage) []string {
	if !json.Valid(input) {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	type object struct {
		names map[string]bool
		key   bool // the next string token is a member name
	}
	var stack []*object
	var repeated []string
	for {
		t, err := d.Token()
		if err != nil {
			return repeated
		}
		var top *object
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t {
		case json.Delim('{'):
			if top != nil {
				top.key = true
			}
			stack = append(stack, &object{names: map[string]bool{}, key: true})
			continue
		case json.Delim('['):
			if top != nil {
				top.key = true
			}
			stack = append(stack, nil)
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
			continue
		}
		if top == nil {
			continue
		}
		if top.key {
			name := t.(string)
			if top.names[name] {
				repeated = append(repeated, name)
			}
			top.names[name] = true
			top.key = false
		} else {
			top.key = true
		}
	}
}
