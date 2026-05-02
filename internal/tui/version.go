package tui

import "runtime/debug"

// kataVersion is the build identifier shown in the title bar. Until we
// start cutting tagged releases, we read the short git commit hash from
// the VCS info Go embeds when building from a git checkout, with a
// -dirty suffix when the working tree had uncommitted changes. Falls
// back to "dev" when no VCS info is available (e.g. building from a
// tarball) so the brand cluster stays visible.
var kataVersion = computeKataVersion()

func computeKataVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
