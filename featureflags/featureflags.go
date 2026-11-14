// Package featureflags defines the registry of feature flags recognized by
// Shelley. Flags are declared at init time via Register; the database stores
// only overrides. Unknown flag rows in the database are ignored.
package featureflags

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Flag describes a single feature flag.
type Flag struct {
	// Name is the primary key. Stable identifier; use kebab-case.
	Name string `json:"name"`
	// Description is shown in the UI.
	Description string `json:"description"`
	// Default is the value when no override is stored. May be any
	// JSON-encodable value, including nil.
	Default any `json:"default"`
}

var (
	mu       sync.RWMutex
	registry = map[string]Flag{}
)

// Register adds a flag to the registry. Panics on duplicate name; intended for
// use in package-level var initializers.
func Register(f Flag) Flag {
	if f.Name == "" {
		panic("featureflags: empty Name")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := registry[f.Name]; ok {
		panic(fmt.Sprintf("featureflags: duplicate flag %q", f.Name))
	}
	// Round-trip Default through JSON to fail fast on non-encodable defaults.
	if _, err := json.Marshal(f.Default); err != nil {
		panic(fmt.Sprintf("featureflags: flag %q has non-JSON Default: %v", f.Name, err))
	}
	registry[f.Name] = f
	return f
}

// All returns every registered flag, sorted by name.
func All() []Flag {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Flag, 0, len(registry))
	for _, f := range registry {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the registered flag and true, or zero and false.
func Lookup(name string) (Flag, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := registry[name]
	return f, ok
}

// Known reports whether a flag with the given name is registered.
func Known(name string) bool {
	_, ok := Lookup(name)
	return ok
}
