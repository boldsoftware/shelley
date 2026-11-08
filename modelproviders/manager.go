package modelproviders

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"shelley.exe.dev/db"
	"shelley.exe.dev/models"
)

var (
	ErrNotFound  = errors.New("model provider not found")
	ErrInvalid   = errors.New("invalid model provider")
	ErrDuplicate = errors.New("a provider with this protocol, endpoint, and API key is already saved")
	ErrConflict  = errors.New("model provider changed while the operation was running")
	ErrDiscovery = errors.New("model discovery failed")
)

type Input struct {
	ProviderType string `json:"provider_type"`
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"api_key"`
	DisplayName  string `json:"display_name"`
}

type Connection struct {
	ProviderID   string `json:"provider_id"`
	ProviderType string `json:"provider_type"`
	Endpoint     string `json:"endpoint"`
	DisplayName  string `json:"display_name"`
	HasAPIKey    bool   `json:"has_api_key"`
	ModelCount   int    `json:"model_count"`
}

type Manager struct {
	db       *db.DB
	registry *models.Manager
	httpc    *http.Client
	mu       sync.Mutex // serializes commit and registry reload, never discovery
}

func New(database *db.DB, registry *models.Manager, httpc *http.Client) *Manager {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Manager{db: database, registry: registry, httpc: httpc}
}

func connection(p db.ModelProvider) Connection {
	return Connection{
		ProviderID: p.ProviderID, ProviderType: p.ProviderType, Endpoint: p.Endpoint,
		DisplayName: p.DisplayName, HasAPIKey: p.APIKey != "", ModelCount: len(p.Models),
	}
}

func (m *Manager) List(ctx context.Context) ([]Connection, error) {
	rows, err := m.db.GetModelProviders(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Connection, 0, len(rows))
	for _, row := range rows {
		result = append(result, connection(row))
	}
	return result, nil
}

// Check discovers models without persisting credentials or changing the catalog.
func (m *Manager) Check(ctx context.Context, id string, input Input) ([]db.ProviderModel, error) {
	var old *db.ModelProvider
	if id != "" {
		var err error
		old, err = m.get(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	p, err := candidate(input, old)
	if err != nil {
		return nil, err
	}
	if p.APIKey == "" {
		return nil, fmt.Errorf("%w: api_key is required", ErrInvalid)
	}
	return m.discover(ctx, p)
}

func (m *Manager) Create(ctx context.Context, input Input) (Connection, error) {
	p, err := candidate(input, nil)
	if err != nil {
		return Connection{}, err
	}
	if p.APIKey == "" {
		return Connection{}, fmt.Errorf("%w: api_key is required", ErrInvalid)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Connection{}, err
	}
	p.ProviderID = hex.EncodeToString(id[:])
	if p.Models, err = m.discover(ctx, p); err != nil {
		return Connection{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.prepare(ctx, &p); err != nil {
		return Connection{}, err
	}
	if err := m.db.CreateModelProvider(ctx, p); err != nil {
		return Connection{}, err
	}
	if err := m.registry.RefreshProviders(); err != nil {
		return Connection{}, err
	}
	return connection(p), nil
}

func (m *Manager) Update(ctx context.Context, id string, input Input) (Connection, error) {
	old, err := m.get(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	p, err := candidate(input, old)
	if err != nil {
		return Connection{}, err
	}
	p.ProviderID = id
	p.Models = old.Models
	configurationChanged := p.ProviderType != old.ProviderType || p.Endpoint != old.Endpoint || p.APIKey != old.APIKey
	if configurationChanged {
		if p.Models, err = m.discover(ctx, p); err != nil {
			return Connection{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.prepare(ctx, &p); err != nil {
		return Connection{}, err
	}
	if err := m.db.UpdateModelProvider(ctx, p, old.Version); err != nil {
		return Connection{}, providerError(err)
	}
	if err := m.registry.RefreshProviders(); err != nil {
		return Connection{}, err
	}
	return connection(p), nil
}

func (m *Manager) Refresh(ctx context.Context, id string) (Connection, error) {
	old, err := m.get(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	p := *old
	if p.Models, err = m.discover(ctx, p); err != nil {
		return Connection{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.db.UpdateModelProvider(ctx, p, old.Version); err != nil {
		return Connection{}, providerError(err)
	}
	if err := m.registry.RefreshProviders(); err != nil {
		return Connection{}, err
	}
	return connection(p), nil
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.db.DeleteModelProvider(ctx, id); err != nil {
		return providerError(err)
	}
	return m.registry.RefreshProviders()
}

func (m *Manager) get(ctx context.Context, id string) (*db.ModelProvider, error) {
	p, err := m.db.GetModelProvider(ctx, id)
	return p, providerError(err)
}

func providerError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, db.ErrProviderConflict):
		return ErrConflict
	default:
		return err
	}
}

// prepare checks duplicates again at commit time, after any network request.
func (m *Manager) prepare(ctx context.Context, p *db.ModelProvider) error {
	rows, err := m.db.GetModelProviders(ctx)
	if err != nil {
		return err
	}
	names := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.ProviderID == p.ProviderID {
			continue
		}
		endpoint, err := canonicalEndpoint(row.Endpoint)
		if err != nil {
			return fmt.Errorf("saved provider %s: %w", row.ProviderID, err)
		}
		if row.ProviderType == p.ProviderType && endpoint == p.Endpoint && row.APIKey == p.APIKey {
			return ErrDuplicate
		}
		names[row.DisplayName] = true
	}
	if p.DisplayName == "" {
		p.DisplayName = sourceLabel(p.Endpoint)
		for n, base := 2, p.DisplayName; names[p.DisplayName]; n++ {
			p.DisplayName = fmt.Sprintf("%s (%d)", base, n)
		}
	}
	return nil
}

func candidate(input Input, old *db.ModelProvider) (db.ModelProvider, error) {
	if input.ProviderType != "anthropic" && input.ProviderType != "openai" && input.ProviderType != "openai-responses" && input.ProviderType != "gemini" {
		return db.ModelProvider{}, fmt.Errorf("%w: unsupported provider_type", ErrInvalid)
	}
	endpoint, err := canonicalEndpoint(input.Endpoint)
	if err != nil {
		return db.ModelProvider{}, err
	}
	key := strings.TrimSpace(input.APIKey)
	if key == "" && old != nil {
		if endpointOrigin(old.Endpoint) != endpointOrigin(endpoint) {
			return db.ModelProvider{}, fmt.Errorf("%w: api_key is required when the endpoint origin changes", ErrInvalid)
		}
		key = old.APIKey
	}
	return db.ModelProvider{
		ProviderType: input.ProviderType, Endpoint: endpoint,
		APIKey: key, DisplayName: strings.TrimSpace(input.DisplayName),
	}, nil
}

func endpointOrigin(raw string) string {
	u, _ := url.Parse(raw)
	return u.Scheme + "://" + u.Host
}

func canonicalEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("%w: endpoint must be an absolute HTTP(S) API base without credentials, query, or fragment", ErrInvalid)
	}
	for _, part := range strings.Split(u.EscapedPath(), "/") {
		part = strings.ToLower(part)
		if part == "." || part == ".." || strings.Contains(part, "%2f") || strings.Contains(part, "%5c") {
			return "", fmt.Errorf("%w: endpoint path is not canonical", ErrInvalid)
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return "", fmt.Errorf("%w: endpoint path is not canonical", ErrInvalid)
		}
	}
	if strings.ContainsAny(u.Path, "\\\x00") {
		return "", fmt.Errorf("%w: endpoint path is not canonical", ErrInvalid)
	}
	path := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/models", "/messages", "/chat/completions", "/responses"} {
		if strings.HasSuffix(strings.ToLower(path), suffix) {
			return "", fmt.Errorf("%w: endpoint must be an API base, not an operation URL", ErrInvalid)
		}
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host += ":" + port
	}
	u.Host, u.Path, u.RawPath = host, path, ""
	return strings.TrimRight(u.String(), "/"), nil
}

func sourceLabel(endpoint string) string {
	u, _ := url.Parse(endpoint)
	return u.Host
}
