package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/skills"
)

// SkillCatalogEntry deliberately excludes instruction bodies.
type SkillCatalogEntry struct {
	Name        string `json:"name" xml:"name"`
	Description string `json:"description" xml:"description"`
	Activate    string `json:"activate" xml:"activate"`
	SourcePath  string `json:"source_path,omitempty" xml:"-"`
	Origin      string `json:"origin,omitempty" xml:"-"`
}

type SkillsResponse struct {
	Skills []SkillCatalogEntry `json:"skills"`
}

func skillCatalogEntry(skill skills.Skill) SkillCatalogEntry {
	origin := skill.Origin
	if origin == "" {
		if skill.Path != "" {
			origin = "File"
		} else {
			origin = "Built into Shelley"
		}
	}
	return SkillCatalogEntry{
		Name: skill.Name, Description: skill.Description, Activate: skill.ActivationCommand(),
		SourcePath: skill.SourceLocation(), Origin: origin,
	}
}

// handleSkills never hydrates a manager: reading a catalog must not create a
// prompt, run hooks, or change the model's conversation snapshot.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	catalog, status, err := s.skillCatalog(r.Context(), r.URL.Query().Get("cwd"), r.URL.Query().Get("conversation_id"))
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, SkillsResponse{Skills: catalog})
}

// skillCatalog returns the skills a conversation's model was told about: the
// current prompt's catalog once started, otherwise a fresh discovery for cwd.
// The int is the HTTP status to report with a non-nil error.
func (s *Server) skillCatalog(ctx context.Context, cwd, conversationID string) ([]SkillCatalogEntry, int, error) {
	var prompt *generated.Message
	if id := conversationID; id != "" {
		var conversation generated.Conversation
		started := false
		err := s.db.Queries(ctx, func(q *generated.Queries) error {
			var err error
			conversation, err = q.GetConversation(ctx, id)
			if err != nil || conversation.IsDraft {
				return err
			}
			// A conversation with no messages yet (e.g. created by /new and
			// never sent) has no prompt; its catalog is still discovery's.
			count, err := q.CountMessagesInConversation(ctx, id)
			if err != nil || count == 0 {
				return err
			}
			started = true
			messages, err := q.ListMessagesByType(ctx, generated.ListMessagesByTypeParams{
				ConversationID: id, Type: string(db.MessageTypeSystem),
			})
			if err != nil {
				return err
			}
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Generation == conversation.CurrentGeneration && !messages[i].ExcludedFromContext {
					prompt = &messages[i]
					break
				}
			}
			return nil
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, http.StatusNotFound, errors.New("Conversation not found")
		}
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("Failed to read conversation: %w", err)
		}
		if started && prompt == nil {
			return nil, http.StatusConflict, errors.New("Conversation has no current system prompt")
		}
	}

	if prompt != nil {
		catalog, err := promptSkillCatalog(*prompt)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("Failed to read prompt skills: %w", err)
		}
		return catalog, http.StatusOK, nil
	}

	if cwd == "" || !filepath.IsAbs(cwd) {
		return nil, http.StatusBadRequest, errors.New("cwd must be an absolute directory path")
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("Invalid cwd: %w", err)
	}
	if !info.IsDir() {
		return nil, http.StatusBadRequest, errors.New("cwd is not a directory")
	}
	// ListAllWithIntegrations resolves the git root when it is empty. Use the
	// same discovery, precedence and environment filter as prompts.
	catalog := make([]SkillCatalogEntry, 0)
	for _, skill := range collectSkills(cwd, "", s.integrationSkills.Skills(ctx), skills.Env{ExeDev: isExeDev()}) {
		catalog = append(catalog, skillCatalogEntry(skill))
	}
	return catalog, http.StatusOK, nil
}

// validateSelectedSkills checks names against the conversation's catalog.
func (s *Server) validateSelectedSkills(ctx context.Context, message, cwd, conversationID string, names []string) ([]string, int, error) {
	if len(names) == 0 {
		return nil, http.StatusOK, nil
	}
	t := strings.TrimSpace(message)
	if t == "" {
		return nil, http.StatusBadRequest, errors.New("add a message to send with the skill")
	}
	if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "!") {
		return nil, http.StatusBadRequest, errors.New("skills can only be attached to a plain message")
	}
	catalog, status, err := s.skillCatalog(ctx, cwd, conversationID)
	if err != nil {
		return nil, status, fmt.Errorf("loading skills: %w", err)
	}
	var valid []string
	for _, name := range names {
		if slices.Contains(valid, name) {
			continue
		}
		if !slices.ContainsFunc(catalog, func(e SkillCatalogEntry) bool { return e.Name == name }) {
			return nil, http.StatusBadRequest, fmt.Errorf("skill %q is not available in this conversation", name)
		}
		valid = append(valid, name)
	}
	return valid, http.StatusOK, nil
}

func withSkillMentions(content []llm.Content, names []string) []llm.Content {
	if len(names) == 0 || len(content) == 0 || content[0].Type != llm.ContentTypeText {
		return content
	}
	content = slices.Clone(content)
	content[0].Text = "Use the following skills: `" + strings.Join(names, "`, `") + "`.\n\n" + content[0].Text
	return content
}

// promptSkillCatalog reads the post-hook prompt, not discovery metadata that
// may describe skills removed or replaced by a hook. Metadata only supplies
// source labels when both the name and activation command still match.
func promptSkillCatalog(prompt generated.Message) ([]SkillCatalogEntry, error) {
	message, err := convertToLLMMessage(prompt)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeText {
			text.WriteString(content.Text)
			text.WriteByte('\n')
		}
	}
	catalog, err := parsePromptSkills(text.String())
	if err != nil {
		return nil, err
	}
	var display SkillsResponse
	if prompt.DisplayData != nil {
		if err := json.Unmarshal([]byte(*prompt.DisplayData), &display); err != nil {
			return nil, fmt.Errorf("invalid system prompt metadata: %w", err)
		}
	}
	for i := range catalog {
		for _, skill := range display.Skills {
			if skill.Name == catalog[i].Name && skill.Activate == catalog[i].Activate {
				catalog[i].SourcePath = skill.SourcePath
				catalog[i].Origin = skill.Origin
				break
			}
		}
	}
	return catalog, nil
}

func parsePromptSkills(prompt string) ([]SkillCatalogEntry, error) {
	catalog := make([]SkillCatalogEntry, 0)
	lines := strings.Split(prompt, "\n")
	// Root guidance is unescaped prose, not XML. Its last closing delimiter
	// belongs to the template, even if AGENTS.md quotes that delimiter itself.
	guidanceStart, guidanceEnd := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case "<guidance>":
			if guidanceStart < 0 {
				guidanceStart = i
			}
		case "</guidance>":
			guidanceEnd = i
		}
	}
	if guidanceStart >= 0 && guidanceEnd < guidanceStart {
		return nil, fmt.Errorf("unterminated guidance block")
	}

	// Both templates emit a standalone outer skills section. Only the final
	// unfenced section outside guidance is authoritative; literal tags and
	// code examples elsewhere in the prompt are not catalogs. Hooks must
	// retain this envelope when replacing the catalog, or remove it entirely.
	start, end := -1, -1
	fence := ""
	for i, line := range lines {
		if guidanceStart >= 0 && i >= guidanceStart && i <= guidanceEnd {
			continue
		}
		line = strings.TrimSpace(line)
		marker := ""
		if len(line) > 0 && (line[0] == '`' || line[0] == '~') {
			marker = line[:len(line)-len(strings.TrimLeft(line, line[:1]))]
		}
		if len(marker) >= 3 {
			if fence == "" {
				fence = marker
			} else if marker[0] == fence[0] && len(marker) >= len(fence) && strings.TrimSpace(line[len(marker):]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		switch line {
		case "<skills>":
			start, end = i, -1
		case "</skills>":
			if start >= 0 && end < 0 {
				end = i
			}
		}
	}
	if start < 0 {
		return catalog, nil
	}
	if end < 0 {
		return nil, fmt.Errorf("unterminated skills section")
	}
	// The envelope's prose is instructions, not XML (it may mention tags).
	// Only the <available_skills> element inside it is the catalog.
	catStart, catEnd := -1, -1
	for i := start + 1; i < end; i++ {
		switch strings.TrimSpace(lines[i]) {
		case "<available_skills>":
			if catStart < 0 {
				catStart = i
			}
		case "</available_skills>":
			catEnd = i
		}
	}
	if catStart < 0 || catEnd < catStart {
		return nil, fmt.Errorf("skills section has no <available_skills> catalog")
	}
	var section struct {
		Skills []SkillCatalogEntry `xml:"skill"`
	}
	if err := xml.Unmarshal([]byte(strings.Join(lines[catStart:catEnd+1], "\n")), &section); err != nil {
		return nil, fmt.Errorf("invalid skills catalog: %w", err)
	}
	seen := make(map[string]bool)
	for _, skill := range section.Skills {
		if strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.Activate) == "" {
			return nil, fmt.Errorf("skill must have a name and activation command")
		}
		if seen[skill.Name] {
			return nil, fmt.Errorf("duplicate prompt skill %q", skill.Name)
		}
		seen[skill.Name] = true
		catalog = append(catalog, skill)
	}
	return catalog, nil
}
