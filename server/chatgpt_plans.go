package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"shelley.exe.dev/siwc"
)

// SetChatGPTPlans enables Sign in with ChatGPT. refreshModels republishes the
// accounts' models after sign-ins and sign-outs.
func (s *Server) SetChatGPTPlans(plans *siwc.Manager, refreshModels func() error) {
	s.chatGPTPlans = plans
	plans.Changed = func() {
		if err := refreshModels(); err != nil {
			s.logger.Warn("failed to publish ChatGPT plan models", "error", err)
		}
		s.notifyModelsChanged()
	}
}

type chatGPTAccounts struct {
	Enabled  bool               `json:"enabled"`
	Accounts []siwc.AccountInfo `json:"accounts"`
}

func (s *Server) handleListChatGPTAccounts(w http.ResponseWriter, r *http.Request) {
	result := chatGPTAccounts{Accounts: []siwc.AccountInfo{}}
	if s.chatGPTPlans != nil {
		accounts, err := s.chatGPTPlans.Accounts(r.Context())
		if err != nil {
			s.logger.Error("failed to list ChatGPT accounts", "error", err)
			http.Error(w, "failed to list ChatGPT accounts", http.StatusInternalServerError)
			return
		}
		result.Enabled, result.Accounts = true, accounts
	}
	writeJSON(w, http.StatusOK, result)
}

// decodeChatGPT reads a small JSON body; an empty body leaves v unchanged.
func decodeChatGPT(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) chatGPTEnabled(w http.ResponseWriter) bool {
	if s.chatGPTPlans == nil {
		http.Error(w, "Sign in with ChatGPT is unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *Server) handleStartChatGPTSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.chatGPTEnabled(w) {
		return
	}
	var input struct {
		AccountID string `json:"account_id"`
		Consent   bool   `json:"consent"`
	}
	if !decodeChatGPT(w, r, &input) {
		return
	}
	signIn, err := s.chatGPTPlans.StartSignIn(r.Context(), input.AccountID, input.Consent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest) // written for the user; never holds credentials
		return
	}
	writeJSON(w, http.StatusOK, signIn)
}

type chatGPTSignInStatus struct {
	Done   bool         `json:"done"`
	Result *siwc.Result `json:"result,omitempty"`
	Error  string       `json:"error,omitempty"`
}

func (s *Server) handleChatGPTSignInStatus(w http.ResponseWriter, r *http.Request) {
	if !s.chatGPTEnabled(w) {
		return
	}
	done, result, err := s.chatGPTPlans.Status(r.PathValue("id"))
	status := chatGPTSignInStatus{Done: done, Result: result}
	if err != nil {
		status.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleCompleteChatGPTSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.chatGPTEnabled(w) {
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if !decodeChatGPT(w, r, &input) {
		return
	}
	result, err := s.chatGPTPlans.CompleteURL(r.Context(), input.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest) // written for the user; never holds credentials
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSignOutChatGPTAccount(w http.ResponseWriter, r *http.Request) {
	if !s.chatGPTEnabled(w) {
		return
	}
	var input struct {
		Remove bool `json:"remove"`
	}
	if !decodeChatGPT(w, r, &input) {
		return
	}
	err := s.chatGPTPlans.SignOut(r.Context(), r.PathValue("id"), input.Remove)
	if errors.Is(err, siwc.ErrNotRevoked) {
		writeJSON(w, http.StatusOK, map[string]string{"warning": siwc.ErrNotRevoked.Error()})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest) // written for the user; never holds credentials
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{})
}
