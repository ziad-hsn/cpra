// Package version identifies official releases and ordinary go build invocations.
package version

import (
	"fmt"
	"runtime/debug"
)

// Official builds inject these fields from RELEASE.json. Date is the source
// commit's UTC timestamp, not wall-clock compilation time.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info returns release identity, falling back to Go's module and VCS metadata
// for builds made without the release tooling.
func Info() string {
	info, _ := debug.ReadBuildInfo()
	return format(Version, Commit, Date, info)
}

func format(version, commit, date string, info *debug.BuildInfo) string {
	if info != nil {
		if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		development := version == "dev"
		modified := false
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if commit == "unknown" {
					commit = setting.Value
				}
			case "vcs.time":
				if date == "unknown" {
					date = setting.Value
				}
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
		if modified && development {
			version += "-dirty"
		}
	}
	return fmt.Sprintf("%s (commit %s, source date %s)", version, commit, date)
}
