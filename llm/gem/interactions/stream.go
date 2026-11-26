package interactions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"shelley.exe.dev/llm"
)

type streamStepState struct {
	step Step

	searchDelta map[string]json.RawMessage
	argsBuf     strings.Builder
}

type streamEvent struct {
	EventType   string          `json:"event_type"`
	Index       int             `json:"index"`
	Step        *Step           `json:"step,omitempty"`
	Delta       json.RawMessage `json:"delta,omitempty"`
	Interaction *Response       `json:"interaction,omitempty"`
	Status      string          `json:"status,omitempty"`
	Error       *streamError    `json:"error,omitempty"`
}

type streamDelta struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	Content     *Content        `json:"content,omitempty"`
	Signature   string          `json:"signature,omitempty"`
	Annotations []Annotation    `json:"annotations,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}

type streamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func parseStreamResponse(ctx context.Context, r io.Reader, onStream func(llm.StreamDelta)) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	var response Response
	steps := make(map[int]*streamStepState)
	var completed bool
	var eventName string
	var data []string

	ensureStep := func(idx int) (*streamStepState, error) {
		if idx < 0 {
			return nil, fmt.Errorf("negative Interactions stream step index %d", idx)
		}
		st := steps[idx]
		if st == nil {
			st = &streamStepState{}
			steps[idx] = st
		}
		return st, nil
	}

	appendText := func(step *Step, text string) *Content {
		if len(step.Content) == 0 || step.Content[len(step.Content)-1].Type != "text" {
			step.Content = append(step.Content, Content{Type: "text"})
		}
		c := &step.Content[len(step.Content)-1]
		c.Text += text
		return c
	}

	appendThought := func(step *Step, text string) {
		if len(step.Summary) == 0 || step.Summary[len(step.Summary)-1].Type != "text" {
			step.Summary = append(step.Summary, Content{Type: "text"})
		}
		step.Summary[len(step.Summary)-1].Text += text
	}

	dispatch := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(data) == 0 {
			eventName = ""
			return nil
		}
		payload := strings.TrimSpace(strings.Join(data, "\n"))
		evName := eventName
		data = data[:0]
		eventName = ""

		if payload == "[DONE]" || evName == "done" {
			return nil
		}

		var ev streamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return fmt.Errorf("decoding Interactions stream event: %w", err)
		}
		evType := ev.EventType
		if evType == "" {
			evType = evName
		}
		if ev.Error != nil || evType == "error" {
			if ev.Error != nil {
				return fmt.Errorf("Interactions stream error (%s): %s", ev.Error.Code, ev.Error.Message)
			}
			return fmt.Errorf("Interactions stream error: %s", payload)
		}

		llm.ReportProgress(ctx)

		switch evType {
		case "interaction.created", "interaction.start":
			if ev.Interaction != nil {
				if ev.Interaction.ID != "" {
					response.ID = ev.Interaction.ID
				}
				if ev.Interaction.Model != "" {
					response.Model = ev.Interaction.Model
				}
				if ev.Interaction.Status != "" {
					response.Status = ev.Interaction.Status
				}
			}
		case "interaction.status_update":
			if ev.Status != "" {
				response.Status = ev.Status
			}
		case "step.start":
			if ev.Step == nil {
				return fmt.Errorf("Interactions step.start missing step payload")
			}
			st, err := ensureStep(ev.Index)
			if err != nil {
				return err
			}
			st.step = *ev.Step

			st.searchDelta = nil
			st.argsBuf.Reset()
			if onStream != nil {
				for _, c := range st.step.Content {
					if c.Type == "text" && c.Text != "" {
						onStream(llm.StreamDelta{Type: "text", Text: c.Text, Index: ev.Index})
					}
				}
				for _, c := range st.step.Summary {
					if c.Type == "text" && c.Text != "" {
						onStream(llm.StreamDelta{Type: "thinking", Text: c.Text, Index: ev.Index})
					}
				}
			}
		case "step.delta":
			if len(ev.Delta) == 0 || bytes.Equal(ev.Delta, []byte("null")) {
				return fmt.Errorf("Interactions step.delta missing delta payload")
			}
			st, err := ensureStep(ev.Index)
			if err != nil {
				return err
			}
			var d streamDelta
			if err := json.Unmarshal(ev.Delta, &d); err != nil {
				return fmt.Errorf("decoding Interactions step delta: %w", err)
			}
			switch d.Type {
			case "image":
				return fmt.Errorf("Gemini Interactions streamed image output is not supported")
			case "text":
				if st.step.Type == "" {
					st.step.Type = "model_output"
				}
				if d.Text != "" {
					appendText(&st.step, d.Text)
					if onStream != nil {
						onStream(llm.StreamDelta{Type: "text", Text: d.Text, Index: ev.Index})
					}
				}
			case "text_annotation_delta":
				if st.step.Type == "" {
					st.step.Type = "model_output"
				}
				if len(d.Annotations) > 0 {
					c := appendText(&st.step, "")
					c.Annotations = append(c.Annotations, d.Annotations...)
				}
			case "thought_summary":
				if st.step.Type == "" {
					st.step.Type = "thought"
				}
				if d.Content != nil && d.Content.Type != "" && d.Content.Type != "text" {
					st.step.Summary = append(st.step.Summary, *d.Content)
					break
				}
				text := d.Text
				if d.Content != nil && d.Content.Text != "" {
					text = d.Content.Text
				}
				if text != "" {
					appendThought(&st.step, text)
					if onStream != nil {
						onStream(llm.StreamDelta{Type: "thinking", Text: text, Index: ev.Index})
					}
				}
			case "thought_signature":
				if st.step.Type == "" {
					st.step.Type = "thought"
				}
				st.step.Signature += d.Signature
			case "arguments_delta":
				if st.step.Type == "" {
					st.step.Type = "function_call"
				}
				if len(d.Arguments) > 0 {
					var chunk string
					if err := json.Unmarshal(d.Arguments, &chunk); err != nil {
						return fmt.Errorf("decoding Interactions arguments_delta: %w", err)
					}
					st.argsBuf.WriteString(chunk)
				}
			case "google_search_call", "google_search_result":
				if st.step.Type == "" {
					st.step.Type = d.Type
				}
				if err := json.Unmarshal(ev.Delta, &st.searchDelta); err != nil {
					return fmt.Errorf("decoding Interactions search delta: %w", err)
				}
				delete(st.searchDelta, "type")
			}
		case "step.stop":
			if st := steps[ev.Index]; st != nil {
				finalizeStep(st)
			}
		case "interaction.completed", "interaction.complete":
			completed = true
			if ev.Interaction != nil {
				if ev.Interaction.ID != "" {
					response.ID = ev.Interaction.ID
				}
				if ev.Interaction.Model != "" {
					response.Model = ev.Interaction.Model
				}
				if ev.Interaction.Status != "" {
					response.Status = ev.Interaction.Status
				}
				if ev.Interaction.Usage != nil {
					response.Usage = ev.Interaction.Usage
				}
				if len(ev.Interaction.Outputs) > 0 {
					response.Outputs = ev.Interaction.Outputs
				}
			}
		}
		return nil
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventName = value
		case "data":
			data = append(data, value)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading Interactions stream: %w", err)
	}
	if len(data) != 0 {
		return nil, fmt.Errorf("unterminated Interactions SSE event: %w", io.ErrUnexpectedEOF)
	}
	if !completed {
		return nil, fmt.Errorf("Interactions stream ended without interaction.completed: %w", io.ErrUnexpectedEOF)
	}
	if len(steps) > 0 && len(response.Outputs) == 0 {
		outputs := make([]Step, 0, len(steps))
		indices := make([]int, 0, len(steps))
		for i := range steps {
			indices = append(indices, i)
		}
		slices.Sort(indices)
		for _, i := range indices {
			st := steps[i]
			finalizeStep(st)
			step, err := st.nativeStep()
			if err != nil {
				return nil, fmt.Errorf("assembling Interactions step %d: %w", i, err)
			}
			outputs = append(outputs, step)
		}
		response.Outputs = outputs
	}
	return &response, nil
}

func finalizeStep(st *streamStepState) {
	if st.argsBuf.Len() > 0 {
		st.step.Arguments = json.RawMessage(st.argsBuf.String())
		st.argsBuf.Reset()
	}
}

// Keep fields from step.start and only replace fields changed by deltas.
func (st *streamStepState) nativeStep() (Step, error) {
	if len(st.step.raw) == 0 && len(st.searchDelta) == 0 {
		return st.step, nil
	}
	native := make(map[string]json.RawMessage)
	var initial stepFields
	if len(st.step.raw) != 0 {
		if err := json.Unmarshal(st.step.raw, &native); err != nil {
			return Step{}, err
		}
		if err := json.Unmarshal(st.step.raw, &initial); err != nil {
			return Step{}, err
		}
	}
	fields := func(step Step) (map[string]json.RawMessage, error) {
		b, err := json.Marshal(stepFields(step))
		if err != nil {
			return nil, err
		}
		var m map[string]json.RawMessage
		err = json.Unmarshal(b, &m)
		return m, err
	}
	before, err := fields(Step(initial))
	if err != nil {
		return Step{}, err
	}
	after, err := fields(st.step)
	if err != nil {
		return Step{}, err
	}
	for k, v := range after {
		if !bytes.Equal(v, before[k]) {
			native[k] = v
		}
	}
	for k, v := range st.searchDelta {
		native[k] = v
	}
	b, err := json.Marshal(native)
	if err != nil {
		return Step{}, err
	}
	var out Step
	err = json.Unmarshal(b, &out)
	return out, err
}
