package server

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" || !filepath.IsAbs(cwd) {
		http.Error(w, "cwd must be an absolute directory path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(cwd)
	if err != nil {
		http.Error(w, "Invalid cwd: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		http.Error(w, "cwd is not a directory", http.StatusBadRequest)
		return
	}

	var prompt *generated.Message
	if id := r.URL.Query().Get("conversation_id"); id != "" {
		var conversation generated.Conversation
		err := s.db.Queries(r.Context(), func(q *generated.Queries) error {
			var err error
			conversation, err = q.GetConversation(r.Context(), id)
			if err != nil || conversation.IsDraft {
				return err
			}
			messages, err := q.ListMessagesByType(r.Context(), generated.ListMessagesByTypeParams{
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
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Failed to read conversation: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !conversation.IsDraft && prompt == nil {
			http.Error(w, "Conversation has no current system prompt", http.StatusConflict)
			return
		}
	}

	catalog := make([]SkillCatalogEntry, 0)
	if prompt != nil {
		catalog, err = promptSkillCatalog(*prompt)
		if err != nil {
			http.Error(w, "Failed to read prompt skills: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// ListAllWithIntegrations resolves the git root when it is empty. Use
		// the same discovery, precedence and environment filter as prompts.
		for _, skill := range collectSkills(cwd, "", s.integrationSkills.Skills(r.Context()), skills.Env{ExeDev: isExeDev()}) {
			catalog = append(catalog, skillCatalogEntry(skill))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SkillsResponse{Skills: catalog})
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
	var section struct {
		Skills []SkillCatalogEntry `xml:"available_skills>skill"`
	}
	if err := xml.Unmarshal([]byte(strings.Join(lines[start:end+1], "\n")), &section); err != nil {
		return nil, fmt.Errorf("invalid skills section: %w", err)
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
