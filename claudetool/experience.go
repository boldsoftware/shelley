package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"shelley.exe.dev/llm"
)

// ExperienceStore keeps durable project knowledge behind a narrow interface so
// tools remain independent of Shelley's database package.
type ExperienceStore interface {
	SearchMemoriesJSON(ctx context.Context, projectPath, query string, limit int) ([]byte, error)
	SaveMemoryFields(ctx context.Context, conversationID, scope, projectPath, kind, title, content string, confidence float64) (string, error)
	AppendCheckpointJSON(ctx context.Context, conversationID, eventType, summary string, state []byte) (string, error)
	LatestCheckpointJSON(ctx context.Context, conversationID string) ([]byte, error)
	ConsolidateDreamJSON(ctx context.Context, conversationID, projectPath, summary string, memories []byte) (int, error)
}

func experienceTextOut(text string) llm.ToolOut {
	return llm.ToolOut{LLMContent: llm.TextContent(text)}
}

type ExperienceTools struct {
	Store          ExperienceStore
	ConversationID string
	WorkingDir     *MutableWorkingDir
}

// ProjectRoot returns the nearest git root, or the original working directory.
func ProjectRoot(workingDir string) string {
	path := workingDir
	for {
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return workingDir
		}
		path = parent
	}
}

func (e *ExperienceTools) projectPath() string {
	return ProjectRoot(e.WorkingDir.Get())
}

type memoryInput struct {
	Action     string  `json:"action"`
	Query      string  `json:"query"`
	Limit      int     `json:"limit"`
	Scope      string  `json:"scope"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
}

var sensitiveMemory = regexp.MustCompile(`(?i)(ghp_[a-z0-9]+|github_pat_[a-z0-9_]+|sk-[a-z0-9_-]{16,}|AKIA[A-Z0-9]{16}|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+|(api[_-]?key|password|passwd|secret|access[_-]?token|authorization)[\s\"']*[:=][\s\"']*[^\s\"']{6,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|bearer\s+[a-z0-9._-]+)`)

func validateMemory(scope, kind, title, content string, confidence float64) error {
	if scope != "project" {
		return fmt.Errorf("scope must be project; global writes require a future explicit user-approval UI")
	}
	switch kind {
	case "fact", "decision", "preference", "lesson":
	default:
		return fmt.Errorf("kind must be fact, decision, preference, or lesson")
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(content) == "" {
		return fmt.Errorf("title and content are required")
	}
	if len(title) > 200 || len(content) > 4000 {
		return fmt.Errorf("memory exceeds size limit")
	}
	if confidence < 0 || confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	if sensitiveMemory.MatchString(title + "\n" + content) {
		return fmt.Errorf("memory rejected because it may contain a credential or secret")
	}
	return nil
}

func (e *ExperienceTools) MemoryTool() *llm.Tool {
	return &llm.Tool{
		Name:        "memory",
		Description: `Search or save durable experience. Search before technical work when prior project decisions may matter. Save only verified, reusable facts, decisions, preferences, or lessons; never save credentials, transient output, or guesses. Memories are isolated by git root; cross-project writes are intentionally unavailable without an explicit user-approval UI.`,
		InputSchema: llm.MustSchema(`{
			"type":"object","required":["action"],"properties":{
				"action":{"type":"string","enum":["search","save"]},
				"query":{"type":"string","description":"Search terms; required for search."},
				"limit":{"type":"integer","minimum":1,"maximum":20},
				"scope":{"type":"string","enum":["project"]},
				"kind":{"type":"string","enum":["fact","decision","preference","lesson"]},
				"title":{"type":"string"},"content":{"type":"string"},
				"confidence":{"type":"number","minimum":0,"maximum":1}
			}}
		`),
		Run: llm.RunJSON(e.runMemory),
	}
}

func (e *ExperienceTools) runMemory(ctx context.Context, in memoryInput) llm.ToolOut {
	switch in.Action {
	case "search":
		data, err := e.Store.SearchMemoriesJSON(ctx, e.projectPath(), in.Query, in.Limit)
		if err != nil {
			return llm.ErrorfToolOut("search memory: %v", err)
		}
		return experienceTextOut(string(data))
	case "save":
		if in.Confidence == 0 {
			in.Confidence = 1
		}
		if err := validateMemory(in.Scope, in.Kind, in.Title, in.Content, in.Confidence); err != nil {
			return llm.ErrorfToolOut("save memory: %v", err)
		}
		project := ""
		if in.Scope == "project" {
			project = e.projectPath()
		}
		id, err := e.Store.SaveMemoryFields(ctx, e.ConversationID, in.Scope, project, in.Kind, in.Title, in.Content, in.Confidence)
		if err != nil {
			return llm.ErrorfToolOut("save memory: %v", err)
		}
		return experienceTextOut("Saved durable memory " + id)
	default:
		return llm.ErrorfToolOut("unknown memory action %q", in.Action)
	}
}

type checkpointState struct {
	Goal         string   `json:"goal"`
	Constraints  []string `json:"constraints"`
	Completed    []string `json:"completed"`
	Open         []string `json:"open"`
	Errors       []string `json:"errors"`
	Verification []string `json:"verification"`
	Next         string   `json:"next"`
}

type journalInput struct {
	Action  string          `json:"action"`
	Summary string          `json:"summary"`
	State   checkpointState `json:"state"`
}

func (e *ExperienceTools) JournalTool() *llm.Tool {
	return &llm.Tool{
		Name:        "task_journal",
		Description: `Write or read the append-only recovery checkpoint for this conversation. Write after meaningful milestones, diagnosed failures, and before risky restarts or context compaction. Keep it compact and factual so work can resume after interruption.`,
		InputSchema: llm.MustSchema(`{
			"type":"object","required":["action"],"properties":{
				"action":{"type":"string","enum":["checkpoint","read"]},
				"summary":{"type":"string","maxLength":1000},
				"state":{"type":"object","properties":{
					"goal":{"type":"string","maxLength":2000},"constraints":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":1000}},
					"completed":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":1000}},"open":{"type":"array","maxItems":100,"items":{"type":"string","maxLength":1000}},
					"errors":{"type":"array","maxItems":50,"items":{"type":"string","maxLength":1000}},"verification":{"type":"array","maxItems":50,"items":{"type":"string","maxLength":1000}},
					"next":{"type":"string","maxLength":2000}
				}}
			}}
		`),
		Run: llm.RunJSON(e.runJournal),
	}
}

func (e *ExperienceTools) runJournal(ctx context.Context, in journalInput) llm.ToolOut {
	if in.Action == "read" {
		data, err := e.Store.LatestCheckpointJSON(ctx, e.ConversationID)
		if err != nil {
			return llm.ErrorfToolOut("read checkpoint: %v", err)
		}
		if len(data) == 0 {
			return experienceTextOut("No checkpoint recorded.")
		}
		return experienceTextOut(string(data))
	}
	if in.Action != "checkpoint" {
		return llm.ErrorfToolOut("unknown task_journal action %q", in.Action)
	}
	if strings.TrimSpace(in.Summary) == "" || len(in.Summary) > 1000 {
		return llm.ErrorfToolOut("checkpoint summary is required and must not exceed 1000 bytes")
	}
	state, _ := json.Marshal(in.State)
	if len(state) > 16*1024 {
		return llm.ErrorfToolOut("checkpoint state must not exceed 16 KiB")
	}
	id, err := e.Store.AppendCheckpointJSON(ctx, e.ConversationID, "checkpoint", in.Summary, state)
	if err != nil {
		return llm.ErrorfToolOut("write checkpoint: %v", err)
	}
	return experienceTextOut("Checkpoint saved " + id)
}

type dreamMemory struct {
	Scope      string  `json:"scope"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
}

type dreamInput struct {
	Summary  string        `json:"summary"`
	Memories []dreamMemory `json:"memories"`
}

func (e *ExperienceTools) DreamTool() *llm.Tool {
	return &llm.Tool{
		Name:        "dream",
		Description: `Consolidate completed, verified work into a short audit record and reusable memories. Call once near the end of a substantial task, after tests. Include only lessons supported by evidence from this task. Empty memories are valid when nothing is reusable.`,
		InputSchema: llm.MustSchema(`{
			"type":"object",
			"required":["summary","memories"],
			"properties":{
				"summary":{"type":"string","maxLength":2000},
				"memories":{
					"type":"array","maxItems":8,
					"items":{
						"type":"object",
						"required":["scope","kind","title","content","confidence"],
						"properties":{
							"scope":{"type":"string","enum":["project"]},
							"kind":{"type":"string","enum":["fact","decision","preference","lesson"]},
							"title":{"type":"string"},
							"content":{"type":"string"},
							"confidence":{"type":"number","minimum":0,"maximum":1}
						}
					}
				}
			}
		}`),
		Run: llm.RunJSON(e.runDream),
	}
}

func (e *ExperienceTools) runDream(ctx context.Context, in dreamInput) llm.ToolOut {
	if strings.TrimSpace(in.Summary) == "" || len(in.Summary) > 2000 {
		return llm.ErrorfToolOut("dream summary is required and must not exceed 2000 bytes")
	}
	project := e.projectPath()
	for i := range in.Memories {
		if in.Memories[i].Confidence == 0 {
			in.Memories[i].Confidence = 1
		}
		m := in.Memories[i]
		if err := validateMemory(m.Scope, m.Kind, m.Title, m.Content, m.Confidence); err != nil {
			return llm.ErrorfToolOut("dream memory %q: %v", m.Title, err)
		}
	}
	memories, err := json.Marshal(in.Memories)
	if err != nil {
		return llm.ErrorfToolOut("encode dream: %v", err)
	}
	saved, err := e.Store.ConsolidateDreamJSON(ctx, e.ConversationID, project, in.Summary, memories)
	if err != nil {
		return llm.ErrorfToolOut("consolidate dream: %v", err)
	}
	return experienceTextOut(fmt.Sprintf("Dream consolidated: %d durable memories saved.", saved))
}
