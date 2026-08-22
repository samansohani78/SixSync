// Package buildinfo exposes the identity of this binary.
//
// Version, Commit and Date are injected at link time (see the Makefile's
// LDFLAGS). When the binary is built without them — `go run`, `go test`, or a
// plain `go build` — they fall back to values read from the embedded module
// metadata, so the output is always honest about what is running rather than
// silently claiming a release version it is not.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Injected via -ldflags "-X". Deliberately not constants.
var (
	version = ""
	commit  = ""
	date    = ""
)

// Unknown is reported for any field that could not be determined. It is a
// distinct sentinel rather than an empty string so that JSON consumers can
// tell "not built with this information" apart from "empty value".
const Unknown = "unknown"

// Info is the full build identity. Field order here is also the field order in
// the JSON output, which callers may depend on for golden tests.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get returns the build identity, filling gaps from the embedded VCS stamps
// that the Go toolchain records automatically.
func Get() Info {
	i := Info{
		Version:   version,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if i.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			i.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if i.Commit == "" {
					i.Commit = s.Value
				}
			case "vcs.time":
				if i.Date == "" {
					i.Date = s.Value
				}
			}
		}
	}

	if i.Version == "" {
		i.Version = Unknown
	}
	if i.Commit == "" {
		i.Commit = Unknown
	}
	if i.Date == "" {
		i.Date = Unknown
	}
	return i
}
