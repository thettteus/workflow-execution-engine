package model_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestVendorTypesDoNotLeak is REQ-MODEL-01's boundary check: no package may
// reference a concrete provider type, so the rest of the engine sees only
// model.Provider / model.Response. We enforce this at the import level — a
// package that cannot import core/model/{openai,anthropic} cannot name their
// types — which is stronger and simpler than an AST type-usage scan.
//
// Exactly three locations may import a concrete provider package: the provider
// package itself, and the single wiring site core/model/providers. Anything else
// is a leak.
func TestVendorTypesDoNotLeak(t *testing.T) {
	const modulePath = "github.com/thettteus/workflow-execution-engine"
	guarded := map[string]bool{
		modulePath + "/core/model/openai":    true,
		modulePath + "/core/model/anthropic": true,
	}
	// Directories (module-relative) allowed to import a guarded package.
	allowedDirs := map[string]bool{
		"core/model/openai":    true,
		"core/model/anthropic": true,
		"core/model/providers": true,
	}

	root := moduleRoot(t)
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "ui" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		relDir, _ := filepath.Rel(root, filepath.Dir(path))
		relDir = filepath.ToSlash(relDir)
		if allowedDirs[relDir] {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if guarded[p] {
				t.Errorf("%s imports guarded provider package %q — concrete provider types must not leak past core/model/providers (REQ-MODEL-01)", relDir+"/"+d.Name(), p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// moduleRoot walks up from the working directory to the dir holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the test directory")
		}
		dir = parent
	}
}
