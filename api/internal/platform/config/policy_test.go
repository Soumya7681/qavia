package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// bannedEnvFuncs read the process environment. Outside this package they are a
// review failure, so the rule is enforced by a test rather than by memory
// (backend-standards.md 6).
var bannedEnvFuncs = map[string]bool{
	"Getenv":    true,
	"LookupEnv": true,
	"Environ":   true,
	"ExpandEnv": true,
}

// skippedDirs hold code we did not write.
var skippedDirs = map[string]bool{
	"gen":    true,
	"bin":    true,
	"vendor": true,
	"tmp":    true,
}

// TestOnlyConfigReadsTheEnvironment walks the whole module. If this fails, the
// fix is to add a settings registry entry, not to add an exception here.
func TestOnlyConfigReadsTheEnvironment(t *testing.T) {
	moduleRoot, err := filepath.Abs("../../..")
	require.NoError(t, err)

	selfDir, err := filepath.Abs(".")
	require.NoError(t, err)

	var offenders []string

	err = filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skippedDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if filepath.Dir(path) == selfDir {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}

		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "os" || !bannedEnvFuncs[sel.Sel.Name] {
				return true
			}
			rel, _ := filepath.Rel(moduleRoot, path)
			offenders = append(offenders,
				rel+":"+fset.Position(sel.Pos()).String()+" os."+sel.Sel.Name)
			return true
		})
		return nil
	})
	require.NoError(t, err)

	require.Empty(t, offenders,
		"os.Getenv and friends may only be called in internal/platform/config. "+
			"Everything else is a setting in the database (backend-standards.md 6). Offenders:\n%s",
		strings.Join(offenders, "\n"))
}
