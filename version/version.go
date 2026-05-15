package version

import (
	"encoding/json"
	"io/fs"
	"os"
	"runtime/debug"

	"shelley.exe.dev/ui"
)

// Version and Tag are set at build time via ldflags
var (
	Version = "dev"
	Tag     = ""
)

// ProtocolVersion is the version of the Shelley HTTP/SSE API contract.
// Bump it when making a breaking change to the API surface (request or
// response shapes, stream event semantics, etc.) so clients can detect
// incompatibility before they try to talk to the server.
const ProtocolVersion = 2

// Capabilities advertises optional, additive features that clients can
// opt into when present. Unlike ProtocolVersion (which gates whether a
// client can talk to the server at all), capabilities are non-breaking:
// a client that doesn't recognize a capability just doesn't use it.
//
// Known capabilities:
//   - "raw_upload": POST /api/upload/raw accepts a raw (non-multipart)
//     request body. Clients that don't see this should use /api/upload.
func Capabilities() []string {
	return []string{"raw_upload"}
}

// Info holds build information from runtime/debug.ReadBuildInfo
type Info struct {
	Version    string `json:"version,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
}

// GetInfo returns build information using runtime/debug.ReadBuildInfo,
// falling back to the embedded build-info.json from the UI build.
// The SHELLEY_VERSION_OVERRIDE environment variable can override the tag for testing.
func GetInfo() Info {
	tag := Tag
	if override := os.Getenv("SHELLEY_VERSION_OVERRIDE"); override != "" {
		tag = override
	}

	info := Info{
		Version: Version,
		Tag:     tag,
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

	// If we didn't get vcs info from debug.ReadBuildInfo, try the embedded build-info.json
	if info.Commit == "" {
		if data, err := fs.ReadFile(ui.Dist, "dist/build-info.json"); err == nil {
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
