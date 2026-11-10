package claudetool

import (
	"context"
	"strings"

	"shelley.exe.dev/llm"
)

func ChapterTool(record func(context.Context, string) (string, error)) *llm.Tool {
	return &llm.Tool{
		Name: "chapter",
		Description: `Emit a short chapter heading for the conversation's sidebar and table of contents, not a message to the user.
At the start of each user turn, compare the user's topic with the latest chapter heading reported by prior chapter tool results. You MUST call this tool when a meaningful new subtopic, question, decision, goal, or approach deserves its own navigation landmark. The overall project need not change: give distinct parts of the same task their own chapters. If there is no current chapter, record the first substantive goal. Do not wait for the user to ask.
Be willing to mark useful topic shifts rather than waiting for a major change of goal. Keep the current chapter for routine implementation steps, tool calls, progress updates, acknowledgements, and tiny corrections.
Use a concrete heading under 50 characters. The server anchors it to the latest user message and prevents headings less than three user messages apart. If skipped, do not retry.
Emit alongside other work when possible; never stop just to write a chapter. Do not announce chapter bookkeeping to the user.`,
		InputSchema: llm.MustSchema(`{"type":"object","properties":{"title":{"type":"string","description":"Short heading for the new direction."}},"required":["title"],"additionalProperties":false}`),
		Sequential:  true,
		Run: llm.RunJSON(func(ctx context.Context, input struct {
			Title string `json:"title"`
		},
		) llm.ToolOut {
			title := strings.TrimSpace(input.Title)
			if title == "" || len([]rune(title)) > 50 {
				return llm.ErrorfToolOut("chapter title must be 1–50 characters")
			}
			note, err := record(ctx, title)
			if err != nil {
				return llm.ErrorToolOut(err)
			}
			return llm.ToolOut{
				LLMContent: llm.TextContent(note),
				Display:    map[string]string{"type": "chapter"},
			}
		}),
	}
}
