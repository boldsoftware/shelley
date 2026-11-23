package interactions

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"shelley.exe.dev/llm"
)

type streamStepState struct {
	present bool
	step    Step
	argsBuf strings.Builder
}

type streamEvent struct {
	EventType   string       `json:"event_type"`
	Index       int          `json:"index"`
	Step        *Step        `json:"step,omitempty"`
	Delta       *streamDelta `json:"delta,omitempty"`
	Interaction *Response    `json:"interaction,omitempty"`
	Status      string       `json:"status,omitempty"`
	Error       *streamError `json:"error,omitempty"`
}

type streamDelta struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	Content     *Content        `json:"content,omitempty"`
	Signature   string          `json:"signature,omitempty"`
	Annotations []Annotation    `json:"annotations,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	SearchType  string          `json:"search_type,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	IsError     bool            `json:"is_error,omitempty"`
}

type streamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func parseStreamResponse(ctx context.Context, r io.Reader, onStream func(llm.StreamDelta)) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	var response Response
	var steps []streamStepState
	var completed bool
	var eventName string
	var data []string

	ensureStep := func(idx int) (*streamStepState, error) {
		if idx < 0 {
			return nil, fmt.Errorf("negative Interactions stream step index %d", idx)
		}
		if idx >= len(steps) {
			grown := make([]streamStepState, idx+1)
			copy(grown, steps)
			steps = grown
		}
		return &steps[idx], nil
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
			st.present = true
			st.step = *ev.Step
			st.step.raw = nil
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
			if ev.Delta == nil {
				return fmt.Errorf("Interactions step.delta missing delta payload")
			}
			st, err := ensureStep(ev.Index)
			if err != nil {
				return err
			}
			st.present = true
			d := ev.Delta
			switch d.Type {
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
			case "google_search_call":
				if st.step.Type == "" {
					st.step.Type = "google_search_call"
				}
				if d.Signature != "" {
					st.step.Signature = d.Signature
				}
				if d.SearchType != "" {
					st.step.SearchType = d.SearchType
				}
				if len(d.Arguments) > 0 {
					st.step.Arguments = append(json.RawMessage(nil), d.Arguments...)
				}
			case "google_search_result":
				if st.step.Type == "" {
					st.step.Type = "google_search_result"
				}
				if d.Signature != "" {
					st.step.Signature = d.Signature
				}
				if len(d.Result) > 0 {
					st.step.Result = append(json.RawMessage(nil), d.Result...)
				}
				st.step.IsError = d.IsError
			}
		case "step.stop":
			if ev.Index >= 0 && ev.Index < len(steps) && steps[ev.Index].present {
				finalizeStep(&steps[ev.Index])
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
				if len(ev.Interaction.Outputs) > 0 && len(steps) == 0 {
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
	if len(steps) > 0 {
		outputs := make([]Step, 0, len(steps))
		for i := range steps {
			if !steps[i].present {
				continue
			}
			finalizeStep(&steps[i])
			outputs = append(outputs, steps[i].step)
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
