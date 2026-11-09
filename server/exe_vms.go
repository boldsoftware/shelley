package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// The sidebar's prod tab (workspace mode with FlagDevProdTabs on) lists the
// user's exe.dev VMs and frames their web pages; a VM's settings
// (exe_vm_settings.go) change it. Each exe.dev integration's key may reach
// only VMs with its tag, so no single integration sees every VM: the list
// merges all of this host's integrations, deduplicated by name, and a VM
// belongs to the first integration that lists it.

// exeVMsTTL is how long a fetched list is served without asking exe.dev.
const exeVMsTTL = 30 * time.Second

type exeVMCache struct {
	mu      sync.Mutex
	list    *exeVMList
	fetched time.Time
	// terminals is access to VMs' web terminals, by VM name.
	terminals map[string]*exeVMTerminalAccess
}

type exeVMList struct {
	VMs []exeVM `json:"vms"`
	// SetupURL, set when this host has no exe.dev integration to list VMs
	// with, is where the user can attach one to it.
	SetupURL  string    `json:"setup_url,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

type exeVM struct {
	Name    string   `json:"name"`
	Emoji   string   `json:"emoji"`
	Status  string   `json:"status"`
	URL     string   `json:"url"`
	SSH     string   `json:"ssh_command"`
	Public  bool     `json:"public"`
	Comment string   `json:"comment"`
	Tags    []string `json:"tags"`
	// Role is "base" for a project's base VM, "conversation" for a conversation's own
	// copy of one, else "".
	Role string `json:"role"`
	// Integration is the exe.dev integration that lists the VM, which its
	// settings change it through.
	Integration string `json:"integration"`
	// Shell says whether the integration may open its terminal
	// (exe_vm_terminal.go).
	Shell bool `json:"-"`
}

// devProdTabs reports whether the sidebar has dev/prod tabs: workspace
// mode with FlagDevProdTabs on.
func (s *Server) devProdTabs(ctx context.Context) (bool, error) {
	if !s.workspaceMode() {
		return false, nil
	}
	return s.flagOn(ctx, FlagDevProdTabs)
}

// requireDevProdTabs answers 404 unless the sidebar has dev/prod tabs.
func (s *Server) requireDevProdTabs(w http.ResponseWriter, r *http.Request) bool {
	on, err := s.devProdTabs(r.Context())
	if err != nil {
		s.internalError(w, "Failed to read feature flags", err)
		return false
	}
	if !on {
		http.NotFound(w, r)
	}
	return on
}

func (s *Server) handleExeVMs(w http.ResponseWriter, r *http.Request) {
	if !s.requireDevProdTabs(w, r) {
		return
	}
	list, err := s.exeVMs(r.Context(), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) exeVMs(ctx context.Context, refresh bool) (*exeVMList, error) {
	c := &s.exeVMCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh && c.list != nil && time.Since(c.fetched) < exeVMsTTL {
		return c.list, nil
	}
	list, err := s.fetchExeVMs(ctx)
	if err != nil {
		return nil, err
	}
	c.list, c.fetched = list, list.FetchedAt
	return list, nil
}

func (s *Server) fetchExeVMs(ctx context.Context) (*exeVMList, error) {
	integrations, err := s.exeDevIntegrationNames(ctx)
	if err != nil {
		return nil, err
	}
	if len(integrations) == 0 {
		host, err := os.Hostname()
		if err != nil {
			return nil, err
		}
		setup := url.Values{"service": {"exe-dev"}, "attach": {"vm:" + host}, "source": {"shelley"}}
		return &exeVMList{VMs: []exeVM{}, SetupURL: "https://exe.dev/integrations/add?" + setup.Encode(), FetchedAt: time.Now()}, nil
	}
	type lsVM struct {
		Name       string `json:"vm_name"`
		Emoji      string `json:"emoji"`
		Status     string `json:"status"`
		HTTPSURL   string `json:"https_url"`
		SSHCommand string `json:"ssh_command"`
		Access     struct {
			Shell bool `json:"shell"`
		} `json:"access"`
		ProxyShare string   `json:"proxy_share"`
		Comment    string   `json:"comment"`
		Tags       []string `json:"tags"`
	}
	lists := make([][]lsVM, len(integrations))
	g, gctx := errgroup.WithContext(ctx)
	for i, integration := range integrations {
		g.Go(func() error {
			out, err := s.exeDev(gctx, integration, "ls --json")
			if err != nil {
				return fmt.Errorf("integration %s: %w", integration, err)
			}
			var ls struct {
				VMs []lsVM `json:"vms"`
			}
			if err := json.Unmarshal(out, &ls); err != nil {
				return fmt.Errorf("integration %s: exe.dev ls: %w", integration, err)
			}
			lists[i] = ls.VMs
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	roles, err := s.exeVMRoles(ctx)
	if err != nil {
		return nil, err
	}
	list := &exeVMList{VMs: []exeVM{}, FetchedAt: time.Now()}
	seen := map[string]bool{}
	for i, vms := range lists {
		for _, vm := range vms {
			if seen[vm.Name] {
				continue
			}
			seen[vm.Name] = true
			list.VMs = append(list.VMs, exeVM{
				Name: vm.Name, Emoji: vm.Emoji, Status: vm.Status, URL: vm.HTTPSURL, SSH: vm.SSHCommand,
				Public: vm.ProxyShare == "public", Comment: vm.Comment, Tags: vm.Tags,
				Role: roles[vm.Name], Integration: integrations[i], Shell: vm.Access.Shell,
			})
		}
	}
	slices.SortFunc(list.VMs, func(a, b exeVM) int { return strings.Compare(a.Name, b.Name) })
	return list, nil
}

// exeVMRoles names the VMs Shelley's projects use as bases and the
// conversation VMs copied from them.
func (s *Server) exeVMRoles(ctx context.Context) (map[string]string, error) {
	roles := map[string]string{}
	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if project.VM != nil && project.VM.Base != "" {
			roles[project.VM.Base] = "base"
		}
	}
	vms, err := s.db.ListConversationVMs(ctx)
	if err != nil {
		return nil, err
	}
	for _, vm := range vms {
		if !vm.Shared {
			roles[vm.Name] = "conversation"
		}
	}
	return roles, nil
}

// handleExeVMFrame reports whether the browser can frame VM name's web page
// on ?origin= (the Shelley page's origin), as a pageFrame.
// It fetches the page as an anonymous visitor would and honors the headers
// exe.dev and the VM's app send; it never works around them.
func (s *Server) handleExeVMFrame(w http.ResponseWriter, r *http.Request) {
	if !s.requireDevProdTabs(w, r) {
		return
	}
	origin, err := url.Parse(r.URL.Query().Get("origin"))
	if err != nil || origin.Host == "" {
		http.Error(w, "origin is required", http.StatusBadRequest)
		return
	}
	vm, ok := s.listedExeVM(w, r)
	if !ok {
		return
	}
	page, err := url.Parse(vm.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	frame, err := probeFrame(r.Context(), page, origin)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, frame)
}

// listedExeVM returns the listed VM of r's {name}, or answers why not.
func (s *Server) listedExeVM(w http.ResponseWriter, r *http.Request) (exeVM, bool) {
	list, err := s.exeVMs(r.Context(), false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return exeVM{}, false
	}
	i := slices.IndexFunc(list.VMs, func(vm exeVM) bool { return vm.Name == r.PathValue("name") })
	if i < 0 {
		http.Error(w, "No such VM", http.StatusNotFound)
		return exeVM{}, false
	}
	return list.VMs[i], true
}
