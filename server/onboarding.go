package server

import (
	"net/http"
	"slices"
)

// exeTextLine is production exe.dev's number for texting Shelley
// (exe.dev/hi links it); other exe.dev environments don't say theirs. A
// number connected to this VM (its messages integration) reaches the
// workspace Assistant; any other gets a link back to connect it, where the
// user picks the VM. exe.dev does the pairing, so a text needs no code.
const exeTextLine = "+16462840896"

// onboarding is what the workspace's first-run cards need from the exe.dev
// environment.
type onboarding struct {
	// TextNumber is the number to text this VM's Assistant at, "" where
	// there is none; TextConnected, whether a number is connected yet.
	TextNumber    string `json:"text_number"`
	TextConnected bool   `json:"text_connected"`
	// ExeDev is the exe-dev integration a web app's VM is created through
	// (this host's default), "" for none; VMHost a VM's hostname, with NAME
	// for the VM's name.
	ExeDev string `json:"exe_dev"`
	VMHost string `json:"vm_host"`
}

// handleOnboarding serves GET /api/onboarding.
func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	env, integrations, err := s.listIntegrations(r.Context())
	if err != nil {
		s.internalError(w, "Failed to list integrations", err)
		return
	}
	var result onboarding
	if env.Production() {
		result.TextNumber = exeTextLine
		result.TextConnected = slices.ContainsFunc(integrations, func(ig reflectionIntegration) bool {
			return ig.Name == messagesIntegration
		})
	}
	var names []string
	for _, ig := range exeDevIntegrationsOf(env, integrations) {
		names = append(names, ig.Name)
	}
	if result.ExeDev = defaultExeDevIntegration(names); result.ExeDev != "" {
		result.VMHost = env.VMHost("NAME")
	}
	writeJSON(w, http.StatusOK, result)
}
