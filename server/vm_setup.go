package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"shelley.exe.dev/db"
)

func (s *Server) exeDevIntegrationNames(ctx context.Context) ([]string, error) {
	env, integrations, err := s.listIntegrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list exe.dev integrations: %w", err)
	}
	names := []string{}
	for _, integration := range exeDevIntegrationsOf(env, integrations) {
		names = append(names, integration.Name)
	}
	return names, nil
}

func (s *Server) handleExeDevIntegrations(w http.ResponseWriter, r *http.Request) {
	names, err := s.exeDevIntegrationNames(r.Context())
	if err != nil {
		s.internalError(w, "Failed to list exe.dev integrations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"integrations": names, "default": defaultExeDevIntegration(names)})
}

// resolveProjectIntegration fills in an empty vm.integration with this
// host's default exe.dev integration.
func (s *Server) resolveProjectIntegration(ctx context.Context, vm *db.ProjectVM) error {
	if vm == nil || vm.Integration != "" {
		return nil
	}
	names, err := s.exeDevIntegrationNames(ctx)
	if err != nil {
		return err
	}
	if vm.Integration = defaultExeDevIntegration(names); vm.Integration != "" {
		return nil
	}
	if len(names) == 0 {
		return fmt.Errorf("%w: set vm.integration: this host has no exe.dev integration to use", db.ErrInvalid)
	}
	return fmt.Errorf("%w: choose vm.integration: this host has several exe.dev integrations (%s)", db.ErrInvalid, strings.Join(names, ", "))
}
