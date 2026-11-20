// Package cache locates the directory the CLI keeps derived data in.
package cache

import (
	"os"
	"path/filepath"
)

// Root returns the cache directory: $XDG_CACHE_HOME/itop-cli, else
// ~/.cache/itop-cli. It returns "" when no home directory is known, and
// callers then skip the cache: a shared fallback such as the system temporary
// directory would let another user plant files the CLI later reads.
func Root() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "itop-cli")
}
