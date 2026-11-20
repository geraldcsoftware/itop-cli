// Package cache locates the directory the CLI keeps derived data in.
package cache

import (
	"os"
	"path/filepath"
)

// Root returns the cache directory: $XDG_CACHE_HOME/itop-cli, else
// ~/.cache/itop-cli.
func Root() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "itop-cli")
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "itop-cli")
}
