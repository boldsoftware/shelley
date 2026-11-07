package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// A VM's settings, the Settings tab of a VM in the prod tab, mirror the VM
// details on exe.dev's dashboard. They read and change the VM through the
// exe.dev integration that lists it, with the exe.dev CLI commands its
// dashboard runs; the browser only ever names one of exeVMChanges.

// exeVMSettings is exe.dev's `ls -l --json` record of a VM (its field
// names) and what this Shelley adds.
type exeVMSettings struct {
	Name        string   `json:"vm_name"`
	Emoji       string   `json:"emoji"`
	Status      string   `json:"status"`
	Region      string   `json:"region_display"`
	Image       string   `json:"image"`
	Pool        string   `json:"pool_name"`
	CreatedAt   string   `json:"created_at"`
	Comment     string   `json:"comment"`
	Tags        []string `json:"tags"`
	URL         string   `json:"https_url"`
	SSH         string   `json:"ssh_command"`
	ShelleyURL  string   `json:"shelley_url"`
	TerminalURL string   `json:"terminal_url"`
	CPUs        int      `json:"allocated_cpus"`
	MemoryBytes int64    `json:"memory_capacity_bytes"`
	DiskBytes   int64    `json:"disk_capacity_bytes"`
	ProxyPort   int      `json:"proxy_port"`
	ProxyShare  string   `json:"proxy_share"`
	Sharing     struct {
		Users []struct {
			Email      string `json:"email"`
			Status     string `json:"status"`
			ShareLevel string `json:"share_level"`
		} `json:"named_user_shares"`
		TeamShared bool `json:"team_shared"`
	} `json:"sharing"`
	ShareLinks []struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	} `json:"share_links"`
	Domains      []string `json:"domains"`
	Egress       string   `json:"egress_policy"`
	EmailEnabled bool     `json:"email_receive_enabled"`
	Access       struct {
		Admin bool `json:"admin"`
	} `json:"access"`

	// EmailAddress is where the VM receives email, when it does.
	EmailAddress string `json:"email_address"`
	Integration  string `json:"integration"`
	// ScopeTag, if set, is the only tag of VMs the integration reaches.
	ScopeTag string `json:"scope_tag"`
	// Role is the VM's role in Shelley's projects (see exeVM).
	Role string `json:"role"`
	// Denied says why a change (an exeVMChanges kind) can't be made here;
	// the others can.
	Denied map[string]string `json:"denied"`
}

// exeVMChange is one change of a VM's settings.
type exeVMChange struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	// Resize's new size; 0 keeps that dimension.
	CPUs     int `json:"cpus"`
	MemoryGB int `json:"memory_gb"`
	DiskGB   int `json:"disk_gb"`
}

// exeVMChanges are the changes a VM's settings make: the exe.dev command
// each runs (as its key permissions name it) and its arguments.
var exeVMChanges = map[string]struct {
	command string
	args    func(vm string, c exeVMChange) ([]string, error)
}{
	"comment": {"comment", func(vm string, c exeVMChange) ([]string, error) {
		if len(c.Value) > 200 || strings.HasPrefix(c.Value, "-") || strings.ContainsAny(c.Value, "\r\n") {
			return nil, errors.New("a comment is one line of at most 200 characters, not starting with -")
		}
		if c.Value == "" {
			// /exec drops an empty argument, and exe.dev trims a comment:
			// a space clears it.
			return []string{vm, " "}, nil
		}
		return []string{vm, c.Value}, nil
	}},
	"tag-add":       {"tag", withWord},
	"tag-remove":    {"tag", func(vm string, c exeVMChange) ([]string, error) { return withFlag("-d", vm, c) }},
	"port":          {"share port", withPort},
	"public":        {"share set-public", vmOnly},
	"private":       {"share set-private", vmOnly},
	"share-add":     {"share add", withEmail},
	"share-remove":  {"share remove", withEmail},
	"link-add":      {"share add-link", vmOnly},
	"link-remove":   {"share remove-link", withWord},
	"domain-add":    {"domain add", withWord},
	"domain-remove": {"domain rm", withWord},
	"egress":        {"egress", oneOf("all", "integrations")},
	"email":         {"share receive-email", oneOf("on", "off")},
	"resize":        {"resize", withSize},
	"rename":        {"rename", withWord},
	"restart":       {"restart", vmOnly},
	"pause":         {"pause", vmOnly},
	"resume":        {"resume", vmOnly},
	"copy":          {"cp", withWord},
	"delete":        {"rm", vmOnly},
}

// exeWord is a single argument: no flag, space or quote trickery.
var exeWord = regexp.MustCompile(`^[A-Za-z0-9_.@*+][A-Za-z0-9_.@*+-]*$`)

func vmOnly(vm string, c exeVMChange) ([]string, error) {
	if c.Value != "" {
		return nil, errors.New(c.Kind + " takes no value")
	}
	return []string{vm}, nil
}

func withWord(vm string, c exeVMChange) ([]string, error) { return withFlag("", vm, c) }

func withFlag(flag, vm string, c exeVMChange) ([]string, error) {
	if !exeWord.MatchString(c.Value) {
		return nil, fmt.Errorf("%q is not a valid value for %s", c.Value, c.Kind)
	}
	if flag != "" {
		return []string{flag, vm, c.Value}, nil
	}
	return []string{vm, c.Value}, nil
}

func withEmail(vm string, c exeVMChange) ([]string, error) {
	if !strings.Contains(c.Value, "@") {
		return nil, fmt.Errorf("%q is not an email address", c.Value)
	}
	return withWord(vm, c)
}

func withPort(vm string, c exeVMChange) ([]string, error) {
	if port, err := strconv.Atoi(c.Value); err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("%q is not a port", c.Value)
	}
	return []string{vm, c.Value}, nil
}

func oneOf(values ...string) func(string, exeVMChange) ([]string, error) {
	return func(vm string, c exeVMChange) ([]string, error) {
		if !slices.Contains(values, c.Value) {
			return nil, fmt.Errorf("%s is one of %s", c.Kind, strings.Join(values, ", "))
		}
		return []string{vm, c.Value}, nil
	}
}

func withSize(vm string, c exeVMChange) ([]string, error) {
	args := []string{vm}
	for _, d := range []struct {
		flag, unit string
		n          int
	}{{"cpu", "", c.CPUs}, {"memory", "GB", c.MemoryGB}, {"disk", "GB", c.DiskGB}} {
		if d.n < 0 {
			return nil, errors.New("sizes are positive")
		}
		if d.n > 0 {
			args = append(args, fmt.Sprintf("--%s=%d%s", d.flag, d.n, d.unit))
		}
	}
	if len(args) == 1 || c.Value != "" {
		return nil, errors.New("resize takes cpus, memory_gb or disk_gb")
	}
	return args, nil
}

// exeVMCommand is the exe.dev command line of change c of vm.
func exeVMCommand(vm string, c exeVMChange) (string, error) {
	change, ok := exeVMChanges[c.Kind]
	if !ok {
		return "", fmt.Errorf("unknown change %q", c.Kind)
	}
	args, err := change.args(vm, c)
	if err != nil {
		return "", err
	}
	return change.command + " " + shellJoin(args) + " --json", nil
}

// exeDevKeyLimits is how far an integration's key reaches, as its page says.
type exeDevKeyLimits struct {
	// Tag, if set, is the only tag of VMs it reaches.
	Tag string
	// Cmds, if not nil, are the only commands it runs (and their
	// subcommands).
	Cmds []string
}

func (l exeDevKeyLimits) allows(command string) bool {
	return l.Cmds == nil || slices.ContainsFunc(l.Cmds, func(c string) bool {
		return c == command || strings.HasPrefix(command, c+" ")
	})
}

// exeDevLimits reads integration's limits from its page (`curl <url>/`).
func (s *Server) exeDevLimits(ctx context.Context, integration string) (exeDevKeyLimits, error) {
	var page string
	if s.conversationVMs.page != nil {
		page = s.conversationVMs.page(integration)
	} else {
		host, err := s.exeDevIntegrationHost(ctx, integration)
		if err != nil {
			return exeDevKeyLimits{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/", nil)
		if err != nil {
			return exeDevKeyLimits{}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return exeDevKeyLimits{}, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			return exeDevKeyLimits{}, err
		}
		if resp.StatusCode != http.StatusOK {
			return exeDevKeyLimits{}, fmt.Errorf("exe.dev integration %s: %s", integration, resp.Status)
		}
		page = string(body)
	}
	var limits exeDevKeyLimits
	for line := range strings.Lines(page) {
		line = strings.TrimSpace(line)
		if tag, ok := strings.CutPrefix(line, "This integration's key reaches only VMs tagged "); ok {
			limits.Tag = strings.TrimSuffix(tag, ".")
		}
		if cmds, ok := strings.CutPrefix(line, "It may run only: "); ok {
			limits.Cmds = strings.Split(strings.TrimSuffix(cmds, "."), ", ")
		}
	}
	return limits, nil
}

// exeDevNotAllowed reports whether err is exe.dev refusing a command the
// key may not run.
func exeDevNotAllowed(err error) bool {
	refusal, ok := errors.AsType[*exeDevError](err)
	return ok && strings.Contains(refusal.Message, "not allowed")
}

func (s *Server) handleExeVMSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireDevProdTabs(w, r) {
		return
	}
	vm, ok := s.listedExeVM(w, r)
	if !ok {
		return
	}
	settings, err := s.exeVMSettings(r.Context(), vm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if settings == nil {
		http.Error(w, "No such VM", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// exeVMSettings reads vm's settings and what may change, or nil if exe.dev
// no longer has it. Read-only commands probe the restrictions a key's page
// doesn't spell out, e.g. commands a tag-scoped key may not run.
func (s *Server) exeVMSettings(ctx context.Context, vm exeVM) (*exeVMSettings, error) {
	integration := vm.Integration
	var (
		ls     struct{ VMs []exeVMSettings }
		limits exeDevKeyLimits
		email  struct {
			Address string `json:"email_address"`
		}
	)
	var mu sync.Mutex
	probes := map[string]error{} // read-only command -> its refusal
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		out, err := s.exeDev(gctx, integration, "ls -l "+shellJoin([]string{vm.Name})+" --json")
		if err == nil {
			err = json.Unmarshal(out, &ls)
		}
		return err
	})
	g.Go(func() (err error) {
		limits, err = s.exeDevLimits(gctx, integration)
		return err
	})
	probe := func(command string, out any) error {
		body, err := s.exeDev(gctx, integration, command+" "+shellJoin([]string{vm.Name})+" --json")
		if exeDevNotAllowed(err) {
			mu.Lock()
			probes[command] = err
			mu.Unlock()
			return nil
		}
		if err != nil || out == nil {
			return err
		}
		return json.Unmarshal(body, out)
	}
	g.Go(func() error { return probe("domain ls", nil) })
	g.Go(func() error { return probe("share receive-email", &email) })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	i := slices.IndexFunc(ls.VMs, func(v exeVMSettings) bool { return v.Name == vm.Name })
	if i < 0 {
		return nil, nil
	}
	settings := &ls.VMs[i]
	settings.Integration, settings.ScopeTag, settings.Role = integration, limits.Tag, vm.Role
	if settings.EmailEnabled {
		settings.EmailAddress = email.Address
	}
	if settings.Tags == nil {
		settings.Tags = []string{}
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	settings.Denied = exeVMDenied(settings, limits, probes, host)
	return settings, nil
}

// exeVMDenied says why each change of settings that can't be made can't.
func exeVMDenied(settings *exeVMSettings, limits exeDevKeyLimits, probes map[string]error, host string) map[string]string {
	denied := map[string]string{}
	for kind, change := range exeVMChanges {
		switch {
		case !settings.Access.Admin:
			denied[kind] = "This VM is shared with you; only its owner can change it."
		case !limits.allows(change.command):
			denied[kind] = fmt.Sprintf("The exe.dev integration %s runs only some commands, so the others are disabled here.", settings.Integration)
		case limits.Tag != "" && change.command == "tag":
			// exe.dev's tag command refuses tag-scoped keys, which no
			// read-only command reveals.
			denied[kind] = fmt.Sprintf("exe.dev doesn't let the integration %s change tags: its key reaches only VMs tagged %s.", settings.Integration, limits.Tag)
		case settings.Name == host && (kind == "delete" || kind == "rename" || kind == "restart" || kind == "pause"):
			denied[kind] = "This Shelley runs on this VM."
		case settings.Role != "" && (kind == "delete" || kind == "rename" || kind == "pause"):
			denied[kind] = "Shelley's projects use this VM; delete their project or conversation instead."
		}
	}
	for _, probe := range []struct {
		command, what string
		kinds         []string
	}{
		{"domain ls", "manage custom domains", []string{"domain-add", "domain-remove"}},
		{"share receive-email", "configure email", []string{"email"}},
	} {
		if probes[probe.command] == nil {
			continue
		}
		why := fmt.Sprintf("exe.dev doesn't let the integration %s %s", settings.Integration, probe.what)
		if limits.Tag != "" {
			why += ": its key reaches only VMs tagged " + limits.Tag
		}
		for _, kind := range probe.kinds {
			if _, ok := denied[kind]; !ok {
				denied[kind] = why + "."
			}
		}
	}
	return denied
}

// handleExeVMChange makes one change of a VM's settings. It checks what the
// settings deny, so a stale page can't make a change it shows disabled.
func (s *Server) handleExeVMChange(w http.ResponseWriter, r *http.Request) {
	if !s.requireDevProdTabs(w, r) {
		return
	}
	var change exeVMChange
	if !decodeStrict(w, r, &change) {
		return
	}
	vm, ok := s.listedExeVM(w, r)
	if !ok {
		return
	}
	command, err := exeVMCommand(vm.Name, change)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	settings, err := s.exeVMSettings(ctx, vm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if settings == nil {
		http.Error(w, "No such VM", http.StatusNotFound)
		return
	}
	if why, ok := settings.Denied[change.Kind]; ok {
		http.Error(w, why, http.StatusForbidden)
		return
	}
	_, err = s.exeDev(ctx, vm.Integration, command)
	// Whatever happened, the list may be stale.
	s.exeVMCache.mu.Lock()
	s.exeVMCache.list = nil
	s.exeVMCache.mu.Unlock()
	if exeDevNotAllowed(err) {
		http.Error(w, fmt.Sprintf("The exe.dev integration %s may not run `%s`.", vm.Integration, exeVMChanges[change.Kind].command), http.StatusForbidden)
		return
	}
	if message, err := exeDevRefusal(err); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	} else if message != "" {
		http.Error(w, "exe.dev refused: "+message, http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
