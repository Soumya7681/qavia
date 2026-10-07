package sourceprovider

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitID is the clone-by-URL provider's ID.
const GitID = "git-clone"

// Git clones a repository into a workspace (BE-6.1).
//
// It shells out to git rather than using a Go implementation, and that is a
// deliberate trade. A pure-Go client would avoid a runtime dependency; what it would
// also do is reimplement partial clones, alternates, LFS, and every server quirk that
// twenty years of git has absorbed, and be subtly wrong about one of them on somebody
// else's monorepo. The dependency is one binary that is already on every machine that
// can build this platform.
//
// Three properties matter more than the choice of client:
//
//   - **The token never appears in a process argument.** It goes into the
//     subprocess's environment and is read by a credential helper, because argv is
//     world-readable on Linux: a URL with a token in it is a token in `ps` output and
//     in any crash dump taken while the clone runs (BE-6.1.3).
//   - **Shallow by default.** Depth is a setting, because the platform needs the
//     current tree and its recent history, not ten years of it.
//   - **The clone happens in the worker.** Nothing about the repository, and
//     certainly no credential, is handed to a runner container.
type Git struct {
	// Depth is how much history to fetch. Zero means the default.
	Depth int

	// Timeout bounds one clone. A repository that has not arrived in this long is
	// either enormous or unreachable, and both are answers.
	Timeout time.Duration

	// Token is the read-only access token, or empty for a public repository. It is
	// held for the duration of one Fetch and never written anywhere.
	Token string
}

// defaults for a clone.
const (
	defaultDepth   = 50
	defaultTimeout = 10 * time.Minute
)

func NewGit(depth int, timeout time.Duration, token string) *Git {
	if depth <= 0 {
		depth = defaultDepth
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Git{Depth: depth, Timeout: timeout, Token: token}
}

func (g *Git) ID() string { return GitID }

// Available reports whether git is on this host. A worker without it can still take
// an archive upload, which is the whole reason the archive provider is the built-in
// one (F-13.6).
func (g *Git) Available(ctx context.Context) bool {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return exec.CommandContext(probe, "git", "--version").Run() == nil
}

// Fetch clones the repository into workspace.
func (g *Git) Fetch(ctx context.Context, req Request, workspace string) (Result, error) {
	if strings.TrimSpace(req.URL) == "" {
		return Result{}, fmt.Errorf("git: no repository URL was given")
	}

	cloneCtx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()

	arguments := []string{
		// The credential helper is a shell function that echoes the token from the
		// environment. Passed with -c so it applies to this invocation only: writing it
		// into a git config file would leave a helper behind that reads an environment
		// variable somebody else might one day set.
		"-c", "credential.helper=" + credentialHelper,

		// No prompting, ever. A clone that asks for a password on a worker with no
		// terminal hangs until the timeout kills it, and the reason is invisible.
		"-c", "core.askPass=",

		// Submodules are not fetched. A submodule is another repository with another
		// URL and possibly another credential, and following one silently would mean
		// this platform fetching a host nobody allowlisted.
		"-c", "submodule.recurse=false",

		"clone",
		"--no-tags",
		"--single-branch",
		fmt.Sprintf("--depth=%d", g.Depth),
	}
	if req.Ref != "" {
		arguments = append(arguments, "--branch", req.Ref)
	}
	arguments = append(arguments, req.URL, workspace)

	// The arguments are built above from a validated URL and integers from settings;
	// the token is in the environment, not here.
	command := exec.CommandContext(cloneCtx, "git", arguments...) //nolint:gosec // see above
	command.Env = g.environment()

	var stderr bytes.Buffer
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		// git writes the useful part to stderr, and it is safe to surface: the token is
		// in the environment, not in the command line or the URL, so a failure message
		// cannot contain it.
		return Result{}, fmt.Errorf("git clone failed: %w: %s", err, excerpt(stderr.String()))
	}

	commit, err := g.head(cloneCtx, workspace)
	if err != nil {
		return Result{}, err
	}

	return Result{Root: workspace, Ref: commit}, nil
}

// credentialHelper is the shell fragment git runs when it needs a credential.
//
// It reads the token from the environment of the subprocess, so the secret is never
// an argument. The username is a constant because every provider that accepts a token
// as a password ignores it: GitHub wants the token as the password with any username,
// and so do GitLab and Bitbucket app passwords.
//
//nolint:gosec // G101: a shell fragment naming an environment variable, not a secret
const credentialHelper = `!f() { echo "username=qavia"; echo "password=${QAVIA_GIT_TOKEN}"; }; f`

// environment is the subprocess's environment, built rather than inherited.
//
// Inheriting the worker's environment would hand a clone the database URL and the
// encryption key. git needs almost nothing, so it gets almost nothing.
func (g *Git) environment() []string {
	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",

		// LFS is not fetched. A repository using it clones its pointers, which is what
		// a comprehension pass needs; downloading gigabytes of binary assets is not.
		"GIT_LFS_SKIP_SMUDGE=1",

		// A minimal PATH, so git can find its own helpers and nothing else finds
		// anything interesting.
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/nonexistent",
	}
	if g.Token != "" {
		env = append(env, "QAVIA_GIT_TOKEN="+g.Token)
	}
	return env
}

// head reads the commit that was actually checked out.
//
// Recorded because a branch name is not a revision: a map of a repository, or a
// coverage number measured against it, has to say which commit it describes.
func (g *Git) head(ctx context.Context, workspace string) (string, error) {
	// Fixed arguments apart from the workspace this package created.
	command := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "HEAD") //nolint:gosec
	command.Env = g.environment()

	var out bytes.Buffer
	command.Stdout = &out

	if err := command.Run(); err != nil {
		return "", fmt.Errorf("read the cloned revision: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

// excerpt keeps a git error readable in a log line and in an API message.
func excerpt(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) > 400 {
		return "…" + trimmed[len(trimmed)-400:]
	}
	return trimmed
}
