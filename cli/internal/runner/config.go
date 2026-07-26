package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
)

// config is the optional wee.yaml a workflow directory may carry to configure
// tool sandboxing. Everything is optional; an absent file yields safe,
// deny-first defaults (empty allowlists, workspace root = current directory).
//
//	# wee.yaml
//	workspaceRoot: .
//	terminal:
//	  allow: [go, git]
//	  timeoutMs: 30000
//	http:
//	  allow: [api.github.com]
type config struct {
	WorkspaceRoot string `yaml:"workspaceRoot"`
	Terminal      struct {
		Allow     []string `yaml:"allow"`
		TimeoutMs int64    `yaml:"timeoutMs"`
	} `yaml:"terminal"`
	HTTP struct {
		Allow []string `yaml:"allow"`
	} `yaml:"http"`
}

// loadConfig reads wee.yaml from dir if present. A missing file is not an error
// — it returns the defaults. WorkspaceRoot defaults to "." (the current
// directory), preserving the original no-config behavior. When wee.yaml exists,
// relative workspaceRoot values are resolved beside that file, so an imported
// template carries its tool sandbox with it instead of depending on where
// `wee serve` happened to start.
func loadConfig(dir string) (*config, error) {
	var c config
	configPath := filepath.Join(dir, "wee.yaml")
	hasConfig := false
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		hasConfig = true
		if err := serialize.UnmarshalYAML(data, &c); err != nil {
			return nil, err
		}
	case os.IsNotExist(err):
		// keep defaults
	default:
		return nil, err
	}
	if c.WorkspaceRoot == "" {
		c.WorkspaceRoot = "."
	}
	expanded, err := expandRequiredEnv(c.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("workspaceRoot: %w", err)
	}
	c.WorkspaceRoot = expanded
	if hasConfig && !filepath.IsAbs(c.WorkspaceRoot) {
		c.WorkspaceRoot = filepath.Join(dir, c.WorkspaceRoot)
	}
	return &c, nil
}

var envDefaultRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*):-([^}]*)\}`)

func expandRequiredEnv(s string) (string, error) {
	s = envDefaultRe.ReplaceAllStringFunc(s, func(match string) string {
		parts := envDefaultRe.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		if v, ok := os.LookupEnv(parts[1]); ok {
			return v
		}
		return parts[2]
	})
	var missing []string
	out := os.Expand(s, func(name string) string {
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("environment variable %q is not set", strings.Join(missing, ", "))
	}
	return out, nil
}

// cacheFor builds the node cache rooted at baseDir.
func cacheFor(baseDir string) *cache.Cache {
	return cache.New(baseDir)
}
