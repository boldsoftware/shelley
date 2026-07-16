// Package exeenv resolves exe.dev environment-specific service URLs.
package exeenv

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Environment describes the scheme and base domain used by exe.dev services.
type Environment struct {
	scheme  string
	boxHost string
}

// FromHostname resolves the exe.dev environment containing hostname.
func FromHostname(hostname string) Environment {
	if strings.Contains(strings.ToLower(hostname), "exe.cloud") {
		return Environment{scheme: "http", boxHost: "exe.cloud"}
	}
	return Environment{scheme: "https", boxHost: "exe.xyz"}
}

var current = sync.OnceValues(func() (Environment, error) {
	out, err := exec.Command("hostname", "-f").Output()
	if err != nil {
		return Environment{}, fmt.Errorf("resolve qualified hostname: %w", err)
	}
	hostname := strings.TrimSpace(string(out))
	if hostname == "" {
		return Environment{}, fmt.Errorf("resolve qualified hostname: empty output")
	}
	return FromHostname(hostname), nil
})

// Current resolves the environment from this machine's qualified hostname.
func Current() (Environment, error) {
	return current()
}

// ReflectionURL returns the reflection integration's base URL.
func (e Environment) ReflectionURL() string {
	return e.scheme + "://reflection.int." + e.boxHost
}

// IntegrationHost returns the hostname for a personal or team integration.
func (e Environment) IntegrationHost(name string, team bool) string {
	scope := "int"
	if team {
		scope = "team"
	}
	return name + "." + scope + "." + e.boxHost
}

// IntegrationURL returns the base URL for a personal or team integration.
func (e Environment) IntegrationURL(name string, team bool) string {
	return e.scheme + "://" + e.IntegrationHost(name, team)
}
