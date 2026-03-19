package forgepath

import (
	"os"
	"path/filepath"
	"sync"
)

const (
	// DependencyConfigFile is the basename of the dependency-map YAML file.
	DependencyConfigFile = "agent-dependencies.yaml"

	// DependencyConfigEnv is the env var that overrides the default config path.
	DependencyConfigEnv = "FORGE_DEPENDENCY_CONFIG"

	// DefaultDependencyConfigPath is the default relative path used when the
	// env var is not set and no CLI flag overrides it.
	DefaultDependencyConfigPath = "conf/" + DependencyConfigFile
)

// DependencyConfigPath returns the dependency config file path, checking the
// FORGE_DEPENDENCY_CONFIG env var first and falling back to the default.
func DependencyConfigPath() string {
	if p := os.Getenv(DependencyConfigEnv); p != "" {
		return p
	}
	return DefaultDependencyConfigPath
}

var (
	mu       sync.Mutex
	override string // set by CLI flag
	cached   string
)

// SetHome sets the forge home directory from the --forge-home CLI flag.
// Must be called before any calls to ForgeHome or Resolve.
func SetHome(path string) {
	mu.Lock()
	defer mu.Unlock()
	override = path
	cached = "" // clear cache so next call re-resolves
}

// ForgeHome returns the resolved forge home directory.
// Resolution order: --forge-home flag > FORGE_HOME env > ~/.forge
func ForgeHome() string {
	mu.Lock()
	defer mu.Unlock()

	if cached != "" {
		return cached
	}

	switch {
	case override != "":
		cached = expandHome(override)
	case os.Getenv("FORGE_HOME") != "":
		cached = expandHome(os.Getenv("FORGE_HOME"))
	default:
		home, _ := os.UserHomeDir()
		if home != "" {
			cached = filepath.Join(home, ".forge")
		} else {
			cached = filepath.Join(os.TempDir(), ".forge")
		}
	}

	return cached
}

// Resolve returns filepath.Join(ForgeHome(), sub).
func Resolve(sub string) string {
	return filepath.Join(ForgeHome(), sub)
}

func expandHome(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if len(path) > 1 && path[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return filepath.Clean(path)
}
