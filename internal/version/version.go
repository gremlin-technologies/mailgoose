// Package version reports the build's version, commit and build date.
//
// Release builds set them with -ldflags, for example:
//
//	go build -ldflags "-X github.com/gremlin-technologies/mailgoose/internal/version.Version=v0.1.0 \
//	  -X github.com/gremlin-technologies/mailgoose/internal/version.Commit=$(git rev-parse --short HEAD) \
//	  -X github.com/gremlin-technologies/mailgoose/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// Without them, Commit and BuildDate fall back to the VCS revision and commit
// time Go embeds in the binary.
package version

import "runtime/debug"

var (
	// Version is the release version, such as v0.1.0, or "dev".
	Version = "dev"
	// Commit is the short git commit the binary was built from.
	Commit = ""
	// BuildDate is when the binary was built, in RFC 3339 UTC.
	BuildDate = ""
)

// Info is the build information shown by `version` commands and /healthz.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}

// Get returns the build information.
func Get() Info {
	commit, date := Commit, BuildDate
	if bi, ok := debug.ReadBuildInfo(); ok && (commit == "" || date == "") {
		for _, s := range bi.Settings {
			switch {
			case s.Key == "vcs.revision" && commit == "" && len(s.Value) >= 7:
				commit = s.Value[:7]
			case s.Key == "vcs.time" && date == "":
				date = s.Value
			}
		}
	}
	return Info{Version: Version, Commit: commit, BuildDate: date}
}
