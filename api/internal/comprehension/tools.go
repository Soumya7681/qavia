// Package comprehension explores a checked-out repository and produces a map of it
// (F-4.4, BE-6.4).
//
// The tools live here rather than in the AI service, and that placement is the
// package's reason to exist. The plan put local file, grep, and glob tools next to
// the agent on the grounds that the repository is already on worker disk and a
// supervised process between a worker and its own filesystem buys nothing
// (ai-architecture.md 5.2). The same argument, followed one step further, puts them
// in Go: the checkout is in a worker's workspace, the path validation that makes
// reading it safe is already there (internal/workspace), and the Python service is a
// separate process that may not share a host.
//
// So the loop is inverted from the plan's shape. The agent answers "what next" one
// step at a time and this package executes the step. What that buys, beyond avoiding
// a shared filesystem: **every path a model asks for is checked by the code that owns
// the filesystem**, and every result is bounded before it becomes prompt input.
//
// Everything here treats the repository as hostile input. It is a client's source
// code, read on the platform's own disk, and a file in it can be a symlink to `/etc`,
// a hundred megabytes on one line, or a binary that would be nonsense in a prompt.
package comprehension

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hyscaler/qavia/api/internal/workspace"
)

// Bounds on what one tool call may return.
//
// Each one exists because the result becomes prompt input, and prompt input is
// something the platform pays for by the token. A file that is a megabyte on one line
// is not a file the agent needs to see whole.
const (
	maxFileBytes   = 64 << 10
	maxFileLines   = 800
	maxGrepMatches = 60
	maxGlobResults = 200
	maxTreeEntries = 4000
	maxLineBytes   = 400
)

// skipNames are directories that are never source. Skipping them is not an
// optimisation: `node_modules` in a mid-sized project is more files than the rest of
// the repository put together, and a tree listing that includes it is a tree listing
// nobody can read.
var skipNames = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true, "target": true,
	"vendor": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true,
	".nuxt": true, ".turbo": true, ".gradle": true, ".mvn": true, "coverage": true,
	".idea": true, ".vscode": true, ".pytest_cache": true, ".mypy_cache": true,
}

// Tools reads a workspace on the agent's behalf.
type Tools struct {
	space *workspace.Workspace
}

func NewTools(space *workspace.Workspace) *Tools { return &Tools{space: space} }

// Tree lists the repository, for the cacheable prefix of every step.
//
// Directories first in path order, capped, and with the never-source directories
// pruned. It is the one thing the agent sees without asking, because an exploration
// that has to discover the file listing by globbing spends its whole budget doing it.
func (t *Tools) Tree() (map[string]any, error) {
	root := t.space.Root()

	var files []string
	truncated := false

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is skipped rather than failing the walk: one bad
			// permission in a repository must not cost the whole map.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil //nolint:nilerr // one unreadable entry must not fail the listing
		}
		if entry.IsDir() {
			if skipNames[entry.Name()] || (strings.HasPrefix(entry.Name(), ".") && path != root) {
				return fs.SkipDir
			}
			return nil
		}
		// Regular files only. A symlink is not followed here, and a device or a socket
		// is not something to list as source.
		if !entry.Type().IsRegular() {
			return nil
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // a path the walk produced that is not under the root is skipped
		}
		if len(files) >= maxTreeEntries {
			truncated = true
			return fs.SkipAll
		}
		files = append(files, relative)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the repository: %w", err)
	}

	sort.Strings(files)

	return map[string]any{
		"files":     files,
		"fileCount": len(files),
		// Stated, so the agent knows the listing is partial rather than believing the
		// repository is smaller than it is.
		"truncated": truncated,
	}, nil
}

// Read returns a file, bounded and text-only.
func (t *Tools) Read(relative string) (string, error) {
	file, err := t.space.Open(relative)
	if err != nil {
		// The refusal is returned to the agent as the tool's result, not as a failure:
		// asking for the wrong path is a mistake to learn from, and the budget already
		// charges for it.
		return "", err
	}
	defer func() { _ = file.Close() }() //nolint:errcheck // read-only

	reader := bufio.NewReader(io.LimitReader(file, maxFileBytes+1))
	out := &strings.Builder{}
	lines := 0

	for lines < maxFileLines {
		line, err := reader.ReadString('\n')
		if line != "" {
			if !utf8.ValidString(line) {
				// Binary. Reported rather than pasted into a prompt as mojibake.
				return "", fmt.Errorf("%s is not a text file", relative)
			}
			lines++
			// Numbered, because the point of reading a file here is to be able to cite a
			// line in the map and in a later analysis.
			fmt.Fprintf(out, "%d\t%s", lines, truncateLine(line))
		}
		if err != nil {
			break
		}
		if out.Len() > maxFileBytes {
			break
		}
	}

	if lines >= maxFileLines || out.Len() > maxFileBytes {
		fmt.Fprintf(out, "\n… truncated at %d lines. Ask for a specific range with grep.\n", lines)
	}
	if out.Len() == 0 {
		return "(empty file)", nil
	}
	return out.String(), nil
}

// Grep searches the repository for a pattern.
//
// The pattern is compiled as a regular expression, and a pattern that does not
// compile is searched as a literal instead. That is deliberate: a model asked for a
// regular expression will sometimes write a substring, and failing the step over it
// would waste an iteration on a formatting mistake.
func (t *Tools) Grep(pattern, include string) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("grep needs a pattern")
	}

	expression, err := regexp.Compile(pattern)
	if err != nil {
		expression = regexp.MustCompile(regexp.QuoteMeta(pattern))
	}

	root := t.space.Root()
	matches := 0
	out := &strings.Builder{}

	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil //nolint:nilerr // one unreadable entry must not fail the walk
		}
		if entry.IsDir() {
			if skipNames[entry.Name()] || (strings.HasPrefix(entry.Name(), ".") && path != root) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if matches >= maxGrepMatches {
			return fs.SkipAll
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // a path the walk produced that is not under the root is skipped
		}
		if include != "" && !globMatch(include, relative) {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() > 4<<20 {
			// A file whose size cannot be read is skipped, and so is one over four
			// megabytes: at that size it is generated, minified, or a lockfile, and
			// grepping it produces matches nobody can act on.
			return nil //nolint:nilerr // see above
		}

		found, searchErr := t.searchFile(relative, expression, maxGrepMatches-matches, out)
		if searchErr != nil {
			// A file that cannot be read contributes no matches. One refused file must not
			// end a search across a repository.
			return nil //nolint:nilerr // see above
		}
		matches += found
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("search the repository: %w", walkErr)
	}

	if matches == 0 {
		// An explicit "nothing" rather than an empty string: the difference between "no
		// matches" and "the tool failed" is one the agent has to be able to see.
		return fmt.Sprintf("no matches for %q", pattern), nil
	}
	if matches >= maxGrepMatches {
		fmt.Fprintf(out, "… stopped at %d matches. Narrow the pattern or use include.\n", maxGrepMatches)
	}
	return out.String(), nil
}

// searchFile appends this file's matches and reports how many it added.
func (t *Tools) searchFile(
	relative string,
	expression *regexp.Regexp,
	remaining int,
	out *strings.Builder,
) (int, error) {
	file, err := t.space.Open(relative)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }() //nolint:errcheck // read-only

	scanner := bufio.NewScanner(io.LimitReader(file, 4<<20))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	found := 0
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if !expression.MatchString(text) {
			continue
		}
		if !utf8.ValidString(text) {
			return found, nil
		}

		fmt.Fprintf(out, "%s:%d: %s\n", relative, line, strings.TrimSpace(truncateLine(text)))
		found++
		if found >= remaining {
			break
		}
	}
	return found, nil
}

// Glob lists files matching a pattern.
func (t *Tools) Glob(pattern string) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("glob needs a pattern")
	}

	root := t.space.Root()
	var found []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil //nolint:nilerr // one unreadable entry must not fail the walk
		}
		if entry.IsDir() {
			if skipNames[entry.Name()] || (strings.HasPrefix(entry.Name(), ".") && path != root) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if len(found) >= maxGlobResults {
			return fs.SkipAll
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // a path the walk produced that is not under the root is skipped
		}
		if globMatch(pattern, relative) {
			found = append(found, relative)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("glob the repository: %w", err)
	}

	if len(found) == 0 {
		return fmt.Sprintf("no files match %q", pattern), nil
	}

	sort.Strings(found)
	return strings.Join(found, "\n"), nil
}

// globMatch matches a path against a pattern, supporting `**`.
//
// filepath.Match does not cross separators, so `src/**/*.ts` would match nothing.
// Rather than depend on a library for this, `**` is translated to a regular
// expression: it is the only pattern feature the agent uses and it is a dozen lines.
func globMatch(pattern, path string) bool {
	// A bare `*.ts` is treated as "anywhere", because that is what a model means by it
	// and matching only the repository root would answer the wrong question.
	if !strings.ContainsAny(pattern, "/") {
		return matchExpression(pattern, filepath.Base(path))
	}
	return matchExpression(pattern, path)
}

func matchExpression(pattern, subject string) bool {
	expression := &strings.Builder{}
	expression.WriteString("^")

	for index := 0; index < len(pattern); index++ {
		switch {
		case strings.HasPrefix(pattern[index:], "**/"):
			// Zero or more directories.
			expression.WriteString("(?:[^/]+/)*")
			index += 2
		case strings.HasPrefix(pattern[index:], "**"):
			expression.WriteString(".*")
			index++
		case pattern[index] == '*':
			expression.WriteString("[^/]*")
		case pattern[index] == '?':
			expression.WriteString("[^/]")
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[index])))
		}
	}
	expression.WriteString("$")

	compiled, err := regexp.Compile(expression.String())
	if err != nil {
		return false
	}
	return compiled.MatchString(subject)
}

// truncateLine bounds one line. A minified bundle is one line of two hundred
// kilobytes, and the first four hundred characters of it say everything the agent
// needs to know about it.
func truncateLine(line string) string {
	trimmed := strings.TrimRight(line, "\r\n")
	if len(trimmed) <= maxLineBytes {
		if strings.HasSuffix(line, "\n") {
			return trimmed + "\n"
		}
		return trimmed + "\n"
	}
	return trimmed[:maxLineBytes] + " …\n"
}
