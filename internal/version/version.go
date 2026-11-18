// Package version reports the build's version and commit.
//
// Release builds set Version and Commit with -ldflags, for example:
//
//	go build -ldflags "-X github.com/gremlin-technologies/mailgoose/internal/version.Version=v0.1.0 \
//	  -X github.com/gremlin-technologies/mailgoose/internal/version.Commit=$(git rev-parse --short HEAD)"
//
// Without them, Commit falls back to the VCS revision Go embeds in the binary.
package version

import "runtime/debug"

var (
	// Version is the release version, such as v0.1.0, or "dev".
	Version = "dev"
	// Commit is the short git commit the binary was built from.
	Commit = ""
)

// Info is the build information shown by `version` commands and /healthz.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// Get returns the build information.
func Get() Info {
	commit := Commit
	if commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					commit = s.Value[:7]
				}
			}
		}
	}
	return Info{Version: Version, Commit: commit}
}
