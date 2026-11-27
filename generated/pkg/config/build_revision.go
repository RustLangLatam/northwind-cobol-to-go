package config

import "runtime/debug"

// BuildRevision is the build identity of the running binary (ADR-0032): the git
// revision Go stamped at build time (`go build` inside the repository records it
// with no ldflags), with "-dirty" when the tree had local changes. A binary with
// no VCS stamp reports the -ldflags Version, and "unknown" when that is unset too.
func BuildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return revisionFrom(nil, Version)
	}
	return revisionFrom(info.Settings, Version)
}

func revisionFrom(settings []debug.BuildSetting, fallback string) string {
	var rev string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	switch {
	case rev != "" && dirty:
		return rev + "-dirty"
	case rev != "":
		return rev
	case fallback != "":
		return fallback
	default:
		return "unknown"
	}
}
