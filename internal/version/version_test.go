package version

import (
	"runtime/debug"
	"testing"
)

func TestIdentity(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.time", Value: "2026-09-11T00:00:00Z"},
	}}
	for _, tt := range []struct {
		name, version, commit, date, want string
		info                              *debug.BuildInfo
	}{
		{"plain", "dev", "unknown", "unknown", "dev (commit unknown, source date unknown)", nil},
		{"module", "dev", "unknown", "unknown", "v1.2.3 (commit abc, source date 2026-09-11T00:00:00Z)", info},
		{"release", "v2.0.0", "def", "2026-01-01T00:00:00Z", "v2.0.0 (commit def, source date 2026-01-01T00:00:00Z)", info},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := format(tt.version, tt.commit, tt.date, tt.info); got != tt.want {
				t.Fatalf("%q != %q", got, tt.want)
			}
		})
	}
}
