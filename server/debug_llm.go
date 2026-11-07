package server

import (
	"net/http"
	"strconv"

	"shelley.exe.dev/llm/llmhttp"
)

// The /debug/llm/ page (debug/llm/) is an always-on view of the recent LLM
// HTTP exchanges held in memory by llmhttp.Recent. Its data, which holds
// prompts, is served under /api/ for the same protection as the rest of the API.

func (s *Server) handleDebugLLMList(w http.ResponseWriter, r *http.Request) {
	type summary struct {
		llmhttp.Exchange
		Slug string `json:"slug"`
	}
	list := llmhttp.Recent.List()
	slugs := map[string]string{"": ""}
	out := make([]summary, len(list))
	for i, e := range list {
		slug, ok := slugs[e.ConversationID]
		if !ok {
			// Not found just means no slug, e.g. a deleted conversation.
			if c, err := s.db.GetConversationByID(r.Context(), e.ConversationID); err == nil && c.Slug != nil {
				slug = *c.Slug
			}
			slugs[e.ConversationID] = slug
		}
		out[i] = summary{Exchange: e, Slug: slug}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDebugLLMGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	e, ok := llmhttp.Recent.Get(id)
	if !ok {
		http.Error(w, "exchange not found (evicted?)", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, e)
}
