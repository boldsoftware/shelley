package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"shelley.exe.dev/llm"
)

var TodoRead = &llm.Tool{
	Name:        "todo_read",
	Description: `Reads the current todo list. Use frequently to track progress and understand what's pending.`,
	InputSchema: llm.EmptySchema(),
	Run:         todoReadRun,
}

var TodoWrite = &llm.Tool{
	Name:        "todo_write",
	Description: todoWriteDescription,
	InputSchema: llm.MustSchema(todoWriteInputSchema),
	Run:         todoWriteRun,
}

const (
	todoWriteDescription = `todo_write: Creates and manages a structured task list for tracking work and communicating progress to users. Use early and often.

Use for:
- multi-step tasks
- complex work
- when users provide multiple requests
- conversations that start trivial but grow in scope
- when users request additional work (directly or via feedback)

Skip for:
- trivial single-step tasks
- purely conversational exchanges

Update dynamically as work evolves - conversations can spawn tasks, simple tasks can become complex, and new discoveries may require additional work.

Rules:
- Update immediately when task states or task list changes
- Only one task "in-progress" at any time
- Each update completely replaces the task list - include all tasks (past and present)
- Never modify or delete completed tasks
- Queued and in-progress tasks may be restructured as understanding evolves
- Tasks should be atomic, clear, precise, and actionable
- If the user adds new tasks: append, don't replace
`

	todoWriteInputSchema = `
{
  "type": "object",
  "required": ["tasks"],
  "properties": {
    "tasks": {
      "type": "array",
      "description": "Array of tasks to write",
      "items": {
        "type": "object",
        "required": ["id", "task", "status"],
        "properties": {
          "id": {
            "type": "string",
            "description": "stable, unique hyphenated slug"
          },
          "task": {
            "type": "string",
            "description": "actionable step in active tense, sentence case, plain text only, displayed to user"
          },
          "status": {
            "type": "string",
            "enum": ["queued", "in-progress", "completed"],
            "description": "current task status"
          }
        }
      }
    }
  }
}
`
)

type TodoItem struct {
	ID     string `json:"id"`
	Task   string `json:"task"`
	Status string `json:"status"`
}

type TodoList struct {
	Items []TodoItem `json:"items"`
}

type todoWriteInput struct {
	Tasks []TodoItem `json:"tasks"`
}

// TodoFilePath returns the path to the todo file for the given session ID.
func TodoFilePath(sessionID string) string {
	if sessionID == "" {
		return "/tmp/shelley_todos.json"
	}
	return filepath.Join("/tmp", sessionID, "todos.json")
}

func todoFilePathForContext(ctx context.Context) string {
	return TodoFilePath(SessionID(ctx))
}

func todoReadRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	todoPath := todoFilePathForContext(ctx)
	content, err := os.ReadFile(todoPath)
	if os.IsNotExist(err) {
		return llm.ToolOut{LLMContent: llm.TextContent("No todo list found. Use todo_write to create one.")}
	}
	if err != nil {
		return llm.ErrorfToolOut("failed to read todo file: %w", err)
	}

	var todoList TodoList
	if err := json.Unmarshal(content, &todoList); err != nil {
		return llm.ErrorfToolOut("failed to parse todo file: %w", err)
	}

	result := fmt.Sprintf(`<todo_list count="%d">`+"\n", len(todoList.Items))
	for _, item := range todoList.Items {
		result += fmt.Sprintf(`  <task id="%s" status="%s">%s</task>`+"\n", item.ID, item.Status, item.Task)
	}
	result += "</todo_list>"

	return llm.ToolOut{LLMContent: llm.TextContent(result)}
}

func todoWriteRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input todoWriteInput
	if err := json.Unmarshal(m, &input); err != nil {
		// LLMs sometimes send the tasks array as a JSON string.
		var raw map[string]json.RawMessage
		if json.Unmarshal(m, &raw) == nil {
			if tasksRaw, ok := raw["tasks"]; ok {
				var tasksStr string
				if json.Unmarshal(tasksRaw, &tasksStr) == nil {
					if json.Unmarshal([]byte(tasksStr), &input.Tasks) == nil {
						err = nil
					}
				}
			}
		}
		if err != nil {
			return llm.ErrorfToolOut("invalid input: %w", err)
		}
	}

	// Validate that only one task is in-progress
	inProgressCount := 0
	for _, task := range input.Tasks {
		if task.Status == "in-progress" {
			inProgressCount++
		}
	}
	if inProgressCount > 1 {
		return llm.ErrorfToolOut("only one task can be 'in-progress' at a time, found %d", inProgressCount)
	}

	todoList := TodoList{Items: input.Tasks}

	todoPath := todoFilePathForContext(ctx)
	if err := os.MkdirAll(filepath.Dir(todoPath), 0o700); err != nil {
		return llm.ErrorfToolOut("failed to create todo directory: %w", err)
	}

	content, err := json.Marshal(todoList)
	if err != nil {
		return llm.ErrorfToolOut("failed to marshal todo list: %w", err)
	}

	if err := os.WriteFile(todoPath, content, 0o600); err != nil {
		return llm.ErrorfToolOut("failed to write todo file: %w", err)
	}

	NotifyTodoChange(ctx)
	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Updated todo list with %d items.", len(input.Tasks)))}
}
