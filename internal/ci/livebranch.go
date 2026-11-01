package ci

import (
	"path"
	"strings"
)

// LiveBranches returns the branch patterns whose builds publish the live spec: the explicit setting
// (comma-separated, globs such as "release/*" allowed), else the platform's default branch, else main and master.
func LiveBranches(setting string, info Info) []string {
	var patterns []string
	for _, p := range strings.Split(setting, ",") {
		if p = strings.TrimSpace(p); p != "" {
			patterns = append(patterns, p)
		}
	}
	if len(patterns) > 0 {
		return patterns
	}
	if info.DefaultBranch != "" {
		return []string{info.DefaultBranch}
	}
	return []string{"main", "master"}
}

// IsLiveBranch reports whether branch matches one of the patterns. An empty branch never matches.
func IsLiveBranch(branch string, patterns []string) bool {
	if branch == "" {
		return false
	}
	for _, p := range patterns {
		if p == branch {
			return true
		}
		if ok, err := path.Match(p, branch); err == nil && ok {
			return true
		}
	}
	return false
}
