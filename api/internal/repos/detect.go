package repos

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Framework detection (BE-6.3).
//
// Deterministic, and that is the requirement rather than a preference. Which test
// framework a repository uses is written down in its own files: `package.json` lists
// it as a dependency, `pyproject.toml` declares it, `pom.xml` names the surefire
// plugin. A model asked the same question would be right most of the time, and "most
// of the time" is the worst accuracy available for a fact that every later stage —
// which image to run, which command to invoke, which coverage report to parse —
// depends on.
//
// The other half of the requirement is honesty. An unrecognised stack is reported as
// unknown **with the files that were inspected**, so an operator can see what the
// platform looked at and override it, rather than reading a confident guess and
// wondering why the generated tests do not run (BE-6.3.3).

// Stack is what a repository is built with.
type Stack struct {
	// Language is the primary language, or empty when detection failed.
	Language string

	// Framework is the test framework this platform would run, using the same names
	// as the generation side so one vocabulary covers both.
	Framework string

	// PackageManager is what installs dependencies: npm, pnpm, yarn, poetry, uv, pip,
	// maven, gradle, go.
	PackageManager string

	// Version is the language version the repository asks for, when it says.
	Version string

	// TestCommand is what this platform would run, taken from the repository's own
	// scripts where it defines one rather than assumed.
	TestCommand string

	// CoverageTool is the repository's own coverage tool, which is the only thing
	// permitted to produce a coverage number (BE-6.6.1).
	CoverageTool string

	// Inspected lists every file the detector read, in order. It is the answer to
	// "why does it think that", and it is what makes an unknown result actionable
	// rather than a shrug.
	Inspected []string

	// Confident is false when nothing recognisable was found. The platform then says
	// unknown and waits for an override instead of guessing.
	Confident bool
}

// Known reports whether detection produced something usable.
func (s Stack) Known() bool { return s.Confident && s.Framework != "" }

// Summary is a sentence for a log line or an API field.
func (s Stack) Summary() string {
	if !s.Known() {
		return fmt.Sprintf("unknown stack after reading %d file(s)", len(s.Inspected))
	}
	parts := []string{s.Language}
	if s.Version != "" {
		parts = append(parts, s.Version)
	}
	parts = append(parts, s.Framework)
	if s.PackageManager != "" {
		parts = append(parts, "via "+s.PackageManager)
	}
	return strings.Join(parts, " ")
}

// maxManifestBytes caps what the detector reads from any one file. A manifest is
// kilobytes; something claiming to be one and being megabytes is not a manifest.
const maxManifestBytes = 1 << 20

// Detect reads a checkout and reports what it is.
//
// Only the root and one level down are inspected. A monorepo has its manifests in
// package directories, and walking a whole repository looking for every package.json
// would find a hundred of them in `node_modules` before it found the real one.
func Detect(root string) (Stack, error) {
	stack := Stack{}

	manifests := []struct {
		file   string
		detect func(*Stack, []byte)
	}{
		{"package.json", fromPackageJSON},
		{"pyproject.toml", fromPyProject},
		{"setup.cfg", fromSetupCfg},
		{"requirements.txt", fromRequirements},
		{"pom.xml", fromPom},
		{"build.gradle", fromGradle},
		{"build.gradle.kts", fromGradle},
		{"go.mod", fromGoMod},
	}

	for _, manifest := range manifests {
		content, err := readCapped(filepath.Join(root, manifest.file))
		if err != nil {
			continue
		}
		stack.Inspected = append(stack.Inspected, manifest.file)
		manifest.detect(&stack, content)

		if stack.Framework != "" {
			break
		}
	}

	// Lockfiles settle which package manager is in use, which the manifest usually
	// does not say. Checked after the manifest, because a lockfile alone identifies no
	// framework.
	for _, lockfile := range []struct{ file, manager string }{
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
		{"bun.lockb", "bun"},
		{"package-lock.json", "npm"},
		{"poetry.lock", "poetry"},
		{"uv.lock", "uv"},
		{"Pipfile.lock", "pipenv"},
	} {
		if exists(filepath.Join(root, lockfile.file)) {
			stack.Inspected = append(stack.Inspected, lockfile.file)
			if stack.PackageManager == "" || isNodeManager(lockfile.manager) == isNodeManager(stack.PackageManager) {
				stack.PackageManager = lockfile.manager
			}
			break
		}
	}

	// Version files are advisory and specific, so they win over a range in a
	// manifest: a repository with .nvmrc is telling you which Node it is tested on.
	for _, version := range []string{".nvmrc", ".node-version", ".python-version", ".tool-versions"} {
		content, err := readCapped(filepath.Join(root, version))
		if err != nil {
			continue
		}
		stack.Inspected = append(stack.Inspected, version)
		if pinned := firstLine(string(content)); pinned != "" && !strings.Contains(pinned, " ") {
			stack.Version = strings.TrimPrefix(pinned, "v")
		}
		break
	}

	stack.Confident = stack.Framework != ""
	if !stack.Confident {
		// One level down, for a monorepo whose root holds only tooling. Bounded to
		// directories that are not obviously dependencies or build output.
		if nested, found := detectNested(root); found {
			nested.Inspected = append(stack.Inspected, nested.Inspected...)
			return nested, nil
		}
	}

	sort.Strings(stack.Inspected)
	return stack, nil
}

// detectNested looks one level down, for a monorepo.
func detectNested(root string) (Stack, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Stack{}, false
	}

	for _, entry := range entries {
		if !entry.IsDir() || skipDirectory(entry.Name()) {
			continue
		}

		nested, err := Detect(filepath.Join(root, entry.Name()))
		if err != nil || !nested.Confident {
			continue
		}

		// Paths are reported relative to the repository root, because that is the only
		// form a later stage can use.
		for index, file := range nested.Inspected {
			nested.Inspected[index] = filepath.Join(entry.Name(), file)
		}
		return nested, true
	}
	return Stack{}, false
}

// skipDirectory names what is never worth descending into: dependencies, build
// output, and version control.
func skipDirectory(name string) bool {
	switch name {
	case "node_modules", ".git", "dist", "build", "target", "vendor", ".venv", "venv",
		"__pycache__", ".next", ".nuxt", "coverage", ".idea", ".vscode":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// packageJSON is the subset that identifies a stack.
type packageJSON struct {
	Engines         map[string]string `json:"engines"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	PackageManager  string            `json:"packageManager"`
}

func fromPackageJSON(stack *Stack, content []byte) {
	var manifest packageJSON
	if err := json.Unmarshal(content, &manifest); err != nil {
		// A manifest that does not parse is a fact worth keeping: the file was
		// inspected, and it told us nothing.
		return
	}

	stack.Language = "javascript"

	dependencies := map[string]string{}
	for name, version := range manifest.Dependencies {
		dependencies[name] = version
	}
	for name, version := range manifest.DevDependencies {
		dependencies[name] = version
	}

	// Ordered by specificity, not popularity: a repository with both Playwright and
	// Jest has browser tests and unit tests, and the browser suite is the one that
	// needs the special image.
	for _, candidate := range []struct{ dependency, framework string }{
		{"@playwright/test", "playwright"},
		{"playwright", "playwright"},
		{"cypress", "cypress"},
		{"vitest", "vitest"},
		{"jest", "jest"},
		{"mocha", "mocha"},
		{"@jest/globals", "jest"},
	} {
		if _, found := dependencies[candidate.dependency]; found {
			stack.Framework = candidate.framework
			break
		}
	}

	if _, found := dependencies["supertest"]; found && stack.Framework != "" {
		// Supertest is a request library rather than a runner, so it refines rather
		// than replaces: the runner still decides which image runs it.
		stack.TestCommand = strings.TrimSpace(manifest.Scripts["test"])
	}

	for _, tool := range []string{"nyc", "c8", "@vitest/coverage-v8", "@vitest/coverage-istanbul"} {
		if _, found := dependencies[tool]; found {
			stack.CoverageTool = tool
			break
		}
	}
	if stack.CoverageTool == "" && stack.Framework == "jest" {
		// Jest ships its own coverage, so a repository using Jest has a coverage tool
		// whether or not it lists one.
		stack.CoverageTool = "jest --coverage"
	}
	if stack.CoverageTool == "" && stack.Framework == "vitest" {
		stack.CoverageTool = "vitest run --coverage"
	}

	if command := strings.TrimSpace(manifest.Scripts["test"]); command != "" {
		stack.TestCommand = command
	}

	if manifest.PackageManager != "" {
		// The corepack field: "pnpm@9.1.0".
		stack.PackageManager = strings.SplitN(manifest.PackageManager, "@", 2)[0]
	}
	if version := manifest.Engines["node"]; version != "" {
		stack.Version = version
	}
	if strings.Contains(string(content), `"typescript"`) {
		stack.Language = "typescript"
	}
}

// pytestMarkers are the strings that identify pytest in a Python manifest, whichever
// of the four ways a project declares its dependencies.
var pytestMarkers = regexp.MustCompile(`(?i)\bpytest\b`)

func fromPyProject(stack *Stack, content []byte) {
	stack.Language = "python"
	text := string(content)

	switch {
	case pytestMarkers.MatchString(text):
		stack.Framework = "pytest"
	case strings.Contains(text, "unittest"):
		stack.Framework = "unittest"
	}

	switch {
	case strings.Contains(text, "[tool.poetry]"):
		stack.PackageManager = "poetry"
	case strings.Contains(text, "[tool.uv]"):
		stack.PackageManager = "uv"
	case strings.Contains(text, "[tool.hatch"):
		stack.PackageManager = "hatch"
	}

	if strings.Contains(text, "coverage") || strings.Contains(text, "pytest-cov") {
		stack.CoverageTool = "pytest-cov"
	}
	if version := pythonRequires.FindStringSubmatch(text); len(version) > 1 {
		stack.Version = strings.TrimSpace(version[1])
	}
}

var pythonRequires = regexp.MustCompile(`requires-python\s*=\s*"([^"]+)"`)

func fromSetupCfg(stack *Stack, content []byte) {
	stack.Language = "python"
	if pytestMarkers.Match(content) {
		stack.Framework = "pytest"
	}
}

func fromRequirements(stack *Stack, content []byte) {
	stack.Language = "python"
	if pytestMarkers.Match(content) {
		stack.Framework = "pytest"
		stack.PackageManager = "pip"
	}
	if strings.Contains(string(content), "pytest-cov") {
		stack.CoverageTool = "pytest-cov"
	}
}

func fromPom(stack *Stack, content []byte) {
	stack.Language = "java"
	stack.PackageManager = "maven"
	text := string(content)

	switch {
	case strings.Contains(text, "junit-jupiter"):
		stack.Framework = "junit5"
	case strings.Contains(text, "junit"):
		stack.Framework = "junit4"
	case strings.Contains(text, "testng"):
		stack.Framework = "testng"
	}
	if strings.Contains(text, "jacoco") {
		stack.CoverageTool = "jacoco"
	}
	if version := mavenJavaVersion.FindStringSubmatch(text); len(version) > 1 {
		stack.Version = version[1]
	}
}

var mavenJavaVersion = regexp.MustCompile(`<maven\.compiler\.(?:release|source)>([^<]+)<`)

func fromGradle(stack *Stack, content []byte) {
	stack.Language = "java"
	stack.PackageManager = "gradle"
	text := string(content)

	switch {
	case strings.Contains(text, "junit-jupiter"), strings.Contains(text, "useJUnitPlatform"):
		stack.Framework = "junit5"
	case strings.Contains(text, "junit"):
		stack.Framework = "junit4"
	case strings.Contains(text, "testng"):
		stack.Framework = "testng"
	}
	if strings.Contains(text, "jacoco") {
		stack.CoverageTool = "jacoco"
	}
	if strings.Contains(text, "kotlin") {
		stack.Language = "kotlin"
	}
}

func fromGoMod(stack *Stack, content []byte) {
	stack.Language = "go"
	stack.PackageManager = "go"

	// Go's test framework is the toolchain, so there is nothing to detect and nothing
	// to be uncertain about: `go test` is the command, and its own cover profile is
	// the coverage tool.
	stack.Framework = "gotest"
	stack.TestCommand = "go test ./..."
	stack.CoverageTool = "go test -coverprofile"

	if version := goVersion.FindStringSubmatch(string(content)); len(version) > 1 {
		stack.Version = version[1]
	}
}

var goVersion = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)`)

// readCapped reads a manifest, refusing anything implausibly large.
func readCapped(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		// A symlink or a device where a manifest should be. Not read, and not an error
		// worth surfacing: the file simply told us nothing.
		return nil, fs.ErrInvalid
	}
	if info.Size() > maxManifestBytes {
		return nil, fs.ErrInvalid
	}

	return os.ReadFile(path) //nolint:gosec // Path built from a caller-owned root and a fixed name.
}

func exists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func isNodeManager(name string) bool {
	switch name {
	case "npm", "pnpm", "yarn", "bun":
		return true
	default:
		return false
	}
}

func firstLine(text string) string {
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return strings.TrimSpace(text)
}
