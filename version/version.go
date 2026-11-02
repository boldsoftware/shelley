package version

import (
	"encoding/json"
	"io/fs"
	"os"
	"runtime/debug"
)

// Version, Tag, and Customized are set at build time via ldflags.
// Customized is set to "true" by 'make build-custom' for user-customized
// builds (see the customizing-shelley skill); Tag then holds the upstream
// release tag the custom branch diverged from.
var (
	Version    = "dev"
	Tag        = ""
	Customized = ""
)

var buildInfoFS fs.FS

// RegisterBuildInfoFS registers an optional filesystem containing
// dist/build-info.json. Packages that already depend on the embedded UI can
// call this to preserve the fallback without making all version consumers
// compile the UI bundle.
func RegisterBuildInfoFS(fsys fs.FS) {
	buildInfoFS = fsys
}

// Capabilities advertises optional, additive features that clients can
// opt into when present. Capabilities are non-breaking: a client that
// doesn't recognize a capability just doesn't use it, and an older
// server that doesn't ship the field is equivalent to advertising none.
// Clients should treat unknown capability strings as no-ops.
func Capabilities() []string {
	return []string{
		// The server accepts a per-request thinking_level override
		// (off, minimal, low, medium, high, xhigh) on converse
		// endpoints. Clients can expose a picker; older
		// servers without this capability silently ignore the field.
		"thinking-levels",
		// Queued transcription commands are persisted as specialized queued
		// messages and continue independently of the submitting HTTP request.
		"queued-transcriptions",
		// "drafts": conversations may have is_draft=true with their body
		// in the draft column instead of messages. Promoted to a normal
		// conversation when POSTed to /api/conversation/<id>/chat.
		// See POST /api/conversations/draft, PUT /api/conversation/<id>/draft.
		"drafts",
		// "live-call-events": a Live call's delegation runs on the server;
		// clients relay Live's events and follow its instructions. See
		// /api/conversation/<id>/live-session/<session>/{stream,events}.
		"live-call-events",
	}
}

// Info holds build information from runtime/debug.ReadBuildInfo
type Info struct {
	Version    string `json:"version,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	Customized bool   `json:"customized,omitempty"`
}

// UserAgent is the User-Agent Shelley sends: "Shelley", with the short
// commit when known.
func UserAgent() string {
	ua := "Shelley"
	if c := GetInfo().Commit; c != "" {
		ua += "/" + c[:min(8, len(c))]
	}
	return ua
}

// GetInfo returns build information using runtime/debug.ReadBuildInfo,
// falling back to a registered build-info.json filesystem when available.
// Environment overrides for testing:
//   - SHELLEY_VERSION_OVERRIDE overrides the tag
//   - SHELLEY_CUSTOMIZED_OVERRIDE=true marks the build as customized
func GetInfo() Info {
	tag := Tag
	if override := os.Getenv("SHELLEY_VERSION_OVERRIDE"); override != "" {
		tag = override
	}
	customized := Customized == "true"
	if os.Getenv("SHELLEY_CUSTOMIZED_OVERRIDE") == "true" {
		customized = true
	}

	info := Info{
		Version:    Version,
		Tag:        tag,
		Customized: customized,
	}

	buildInfo, ok := debug.ReadBuildInfo()
	if ok {
		for _, setting := range buildInfo.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Commit = setting.Value
			case "vcs.time":
				info.CommitTime = setting.Value
			}
		}
	}

	// If we didn't get vcs info from debug.ReadBuildInfo, try the registered
	// build-info.json filesystem.
	if info.Commit == "" && buildInfoFS != nil {
		if data, err := fs.ReadFile(buildInfoFS, "dist/build-info.json"); err == nil {
			var buildJSON struct {
				Commit     string `json:"commit"`
				CommitTime string `json:"commitTime"`
			}
			if json.Unmarshal(data, &buildJSON) == nil {
				info.Commit = buildJSON.Commit
				info.CommitTime = buildJSON.CommitTime
			}
		}
	}

	return info
}
