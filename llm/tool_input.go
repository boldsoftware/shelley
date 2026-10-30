package llm

import (
	"bytes"
	"encoding/json"
)

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
