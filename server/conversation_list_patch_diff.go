package server

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// computeListPatch generates a granular RFC 6902 JSON Patch transforming
// oldList into newList, treating items as objects keyed by conversation_id.
// Top-level fields are diffed individually; everything else is replaced wholesale.
func computeListPatch(oldList, newList []ConversationWithState) ([]conversationListPatchOp, error) {
	if len(oldList) == 0 && len(newList) == 0 {
		return nil, nil
	}

	working := make([]ConversationWithState, len(oldList))
	copy(working, oldList)

	var ops []conversationListPatchOp

	// First, place every new[j] at position j in working, by id.
	for j, target := range newList {
		i := indexOfID(working, target.ConversationID)
		switch {
		case i == -1:
			value, err := json.Marshal(target)
			if err != nil {
				return nil, err
			}
			ops = append(ops, conversationListPatchOp{
				Op:    "add",
				Path:  fmt.Sprintf("/%d", j),
				Value: value,
			})
			working = append(working, ConversationWithState{})
			copy(working[j+1:], working[j:])
			working[j] = target
		case i != j:
			ops = append(ops, conversationListPatchOp{
				Op:   "move",
				From: fmt.Sprintf("/%d", i),
				Path: fmt.Sprintf("/%d", j),
			})
			item := working[i]
			working = append(working[:i], working[i+1:]...)
			working = append(working, ConversationWithState{})
			copy(working[j+1:], working[j:])
			working[j] = item
			fieldOps, err := fieldDiffOps(j, working[j], target)
			if err != nil {
				return nil, err
			}
			ops = append(ops, fieldOps...)
			working[j] = target
		default:
			fieldOps, err := fieldDiffOps(j, working[j], target)
			if err != nil {
				return nil, err
			}
			ops = append(ops, fieldOps...)
			working[j] = target
		}
	}

	// Trim any stale entries off the end, in descending order.
	for k := len(working) - 1; k >= len(newList); k-- {
		ops = append(ops, conversationListPatchOp{
			Op:   "remove",
			Path: fmt.Sprintf("/%d", k),
		})
	}

	return ops, nil
}

func indexOfID(list []ConversationWithState, id string) int {
	for i, item := range list {
		if item.ConversationID == id {
			return i
		}
	}
	return -1
}

// fieldDiffOps emits a replace op per differing top-level JSON field.
func fieldDiffOps(idx int, oldItem, newItem ConversationWithState) ([]conversationListPatchOp, error) {
	oldFields, err := marshalAsObject(oldItem)
	if err != nil {
		return nil, err
	}
	newFields, err := marshalAsObject(newItem)
	if err != nil {
		return nil, err
	}

	var ops []conversationListPatchOp
	for key, newVal := range newFields {
		oldVal, ok := oldFields[key]
		if ok && bytes.Equal(oldVal, newVal) {
			continue
		}
		op := "replace"
		if !ok {
			// `omitempty` fields are absent from oldFields when the prior
			// value was zero. RFC 6902 `replace` requires the target to
			// exist, so use `add` to materialize the key.
			op = "add"
		}
		ops = append(ops, conversationListPatchOp{
			Op:    op,
			Path:  fmt.Sprintf("/%d/%s", idx, jsonPointerEscape(key)),
			Value: append(json.RawMessage(nil), newVal...),
		})
	}
	for key := range oldFields {
		if _, ok := newFields[key]; !ok {
			ops = append(ops, conversationListPatchOp{
				Op:   "remove",
				Path: fmt.Sprintf("/%d/%s", idx, jsonPointerEscape(key)),
			})
		}
	}
	return ops, nil
}

func marshalAsObject(v any) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// jsonPointerEscape per RFC 6901: '~' → '~0', '/' → '~1'.
func jsonPointerEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '~':
			out = append(out, '~', '0')
		case '/':
			out = append(out, '~', '1')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
