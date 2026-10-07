// Command runnercheck proves the runner's isolation on the host it runs on.
//
// It exists because BE-4.1 and BE-4.4 are "done when the isolation is demonstrated,
// not assumed". Every property this platform claims about a run — no egress by
// default, no metadata endpoint, no reach beyond the allowlist, limits that kill,
// nothing left behind — is a claim about the host's kernel and firewall, not about
// Go code, so it has to be checked where it runs.
//
// Run it on a freshly provisioned runner host before that host executes anything
// for a real client, and again after any change to the runtime, the network, or the
// firewall:
//
//	runnercheck                                 # egress denial, limits, reaping
//	runnercheck -allow example.com=93.184.216.34 # plus: allowlisted target reachable
//	runnercheck -runtime runsc                  # against gVisor
//
// A failing probe means the boundary is not there. It exits non-zero so a
// provisioning pipeline stops.
//
// One thing to know when choosing -allow: an address that belongs to another Docker
// bridge network on the same host is unreachable regardless of the allowlist,
// because Docker's own isolation rules drop traffic between its bridges. A probe
// target has to be outside Docker, or the "allowlisted target reachable" check fails
// for a reason that has nothing to do with this platform.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hyscaler/qavia/api/internal/runner"
)

func main() {
	var (
		image   = flag.String("image", "alpine:3.20", "probe image: needs a shell and busybox wget")
		helper  = flag.String("helper-image", "ghcr.io/hyscaler/qavia-net-helper:build", "firewall helper image")
		runtime = flag.String("runtime", "runc", "container runtime: runsc for gVisor, runc for a laptop")
		allow   = flag.String("allow", "", "comma-separated host=ip pairs the probe run may reach")
		blocked = flag.String("blocked", "1.1.1.1", "an address no run should ever reach")
		verbose = flag.Bool("v", false, "print each probe's container output")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	driver, err := runner.NewDocker(*helper)
	if err != nil {
		fail("connect to the container runtime: %v", err)
	}
	defer func() {
		if err := driver.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "close the runtime client: %v\n", err)
		}
	}()

	if !driver.Available(ctx) {
		fail("the container runtime is not reachable, so nothing can be proven here")
	}

	allowed, err := parseAllow(*allow)
	if err != nil {
		fail("%v", err)
	}

	suite := &checker{
		driver:  driver,
		image:   *image,
		runtime: runner.Runtime(*runtime),
		allowed: allowed,
		blocked: *blocked,
		verbose: *verbose,
	}

	suite.workspaceDelivered(ctx)
	suite.reportCollected(ctx)
	suite.egressDenied(ctx)
	suite.metadataBlocked(ctx)
	if len(allowed) > 0 {
		suite.allowlistReachable(ctx)
		suite.allowlistIsExclusive(ctx)
		suite.metadataBlockedWithTarget(ctx)
	} else {
		suite.skip("allowlisted target reachable", "pass -allow host=ip to check this")
		suite.skip("non-allowlisted host rejected", "pass -allow host=ip to check this")
	}
	suite.wallClockKills(ctx)
	suite.memoryLimitKills(ctx)
	suite.processLimitHolds(ctx)
	suite.nothingLeftBehind(ctx)

	fmt.Printf("\n%d passed, %d failed, %d skipped\n", suite.passed, suite.failed, suite.skipped)
	if suite.failed > 0 {
		fmt.Println("\nThe runner is not isolated as claimed. Do not run client work on this host.")
		os.Exit(1)
	}
}

type checker struct {
	driver  *runner.Docker
	image   string
	runtime runner.Runtime
	allowed []runner.HostAddress
	blocked string
	verbose bool

	passed, failed, skipped int
}

// probe runs one container and returns what happened.
func (c *checker) probe(ctx context.Context, name string, spec runner.Spec) (runner.Result, string) {
	spec.RunID = fmt.Sprintf("check-%s-%d", name, time.Now().UnixNano())
	spec.Runtime = c.runtime

	output := &strings.Builder{}
	result, err := c.driver.Run(ctx, spec, io.Discard)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return result, fmt.Sprintf("the probe could not run: %v", err)
	}
	output.WriteString(result.Logs)
	if c.verbose {
		fmt.Printf("    ---- %s ----\n%s\n    ----\n", name, strings.TrimSpace(result.Logs))
	}
	return result, ""
}

func (c *checker) shell(script string, limits runner.Limits, egress runner.Egress) runner.Spec {
	return runner.Spec{
		Image:   c.image,
		Command: []string{"sh", "-c", script},
		Limits:  limits,
		Egress:  egress,
	}
}

// workspaceDelivered checks the suite actually arrives inside the container.
//
// It is the first probe because everything else assumes it: the workspace is streamed
// in over stdin and extracted by the container itself, and a silent failure there
// looks exactly like a suite with no tests.
func (c *checker) workspaceDelivered(ctx context.Context) {
	spec := c.shell("cat /workspace/hello/probe.txt",
		runner.Limits{Timeout: 30 * time.Second}, runner.Egress{})
	spec.Workspace = map[string]string{"hello/probe.txt": "QAVIA_WORKSPACE_OK\n"}

	result, problem := c.probe(ctx, "workspace", spec)

	c.assert("the workspace is delivered into the container", problem,
		strings.Contains(result.Logs, "QAVIA_WORKSPACE_OK"),
		"a file written into the workspace was not readable inside the container: "+excerpt(result.Logs))
}

// reportCollected checks the other half: a report the container writes has to come
// back out, from a tmpfs that stops existing when the container does.
func (c *checker) reportCollected(ctx context.Context) {
	spec := c.shell(
		"mkdir -p /workspace/.qavia && "+
			`printf '{"schema":"qavia.run/1","framework":"probe","results":[]}' > /workspace/.qavia/report.json`,
		runner.Limits{Timeout: 30 * time.Second}, runner.Egress{})
	spec.Reports = []string{".qavia/report.json"}

	result, problem := c.probe(ctx, "report", spec)

	report := string(result.Reports[".qavia/report.json"])
	c.assert("a report written inside the container is collected", problem,
		strings.Contains(report, `"framework":"probe"`),
		fmt.Sprintf("the report did not come back (%d collected): %s",
			len(result.Reports), excerpt(result.Logs)))
}

// egressDenied is the default state: a run with no allowlisted target has no
// network interface at all, so it cannot reach anything.
func (c *checker) egressDenied(ctx context.Context) {
	result, problem := c.probe(ctx, "egress-denied", c.shell(
		fmt.Sprintf("wget -q -T 4 -O - http://%s/ 2>&1 || echo QAVIA_BLOCKED", c.blocked),
		runner.Limits{Timeout: 30 * time.Second}, runner.Egress{}))

	c.assert("no egress by default", problem,
		strings.Contains(result.Logs, "QAVIA_BLOCKED"),
		fmt.Sprintf("a run with no allowlist reached %s: %s", c.blocked, excerpt(result.Logs)))
}

// metadataBlocked is F-7.5. The address serves instance credentials on every major
// cloud, and a run must not be able to ask for them.
func (c *checker) metadataBlocked(ctx context.Context) {
	result, problem := c.probe(ctx, "metadata-blocked", c.shell(
		"wget -q -T 4 -O - http://169.254.169.254/latest/meta-data/ 2>&1 || echo QAVIA_BLOCKED",
		runner.Limits{Timeout: 30 * time.Second}, runner.Egress{}))

	c.assert("cloud metadata endpoint unreachable", problem,
		strings.Contains(result.Logs, "QAVIA_BLOCKED"),
		"a run read the instance metadata endpoint: "+excerpt(result.Logs))
}

// allowlistReachable is the other half: denial that also denies the target is not a
// working platform.
func (c *checker) allowlistReachable(ctx context.Context) {
	target := c.allowed[0]
	result, problem := c.probe(ctx, "allowlist-reachable", c.shell(
		fmt.Sprintf("wget -q -T 8 -O /dev/null http://%s/ && echo QAVIA_REACHED || "+
			"wget -q -T 8 -O /dev/null https://%s/ && echo QAVIA_REACHED", target.Host, target.Host),
		runner.Limits{Timeout: 60 * time.Second}, runner.Egress{AllowedHosts: c.allowed}))

	c.assert("allowlisted target reachable", problem,
		strings.Contains(result.Logs, "QAVIA_REACHED"),
		fmt.Sprintf("the allowlisted target %s (%s) was not reachable, so the rules are too tight: %s",
			target.Host, target.IP, excerpt(result.Logs)))
}

// allowlistIsExclusive is the rule that matters: allowing one address must not allow
// the internet.
func (c *checker) allowlistIsExclusive(ctx context.Context) {
	result, problem := c.probe(ctx, "allowlist-exclusive", c.shell(
		fmt.Sprintf("wget -q -T 4 -O - http://%s/ 2>&1 || echo QAVIA_BLOCKED", c.blocked),
		runner.Limits{Timeout: 30 * time.Second}, runner.Egress{AllowedHosts: c.allowed}))

	c.assert("non-allowlisted host rejected", problem,
		strings.Contains(result.Logs, "QAVIA_BLOCKED"),
		fmt.Sprintf("a run with a target allowlist also reached %s: %s", c.blocked, excerpt(result.Logs)))
}

// metadataBlockedWithTarget repeats the metadata probe on the network a real run
// uses, because that is the network where a rule could be missing.
func (c *checker) metadataBlockedWithTarget(ctx context.Context) {
	result, problem := c.probe(ctx, "metadata-with-target", c.shell(
		"wget -q -T 4 -O - http://169.254.169.254/latest/meta-data/ 2>&1 || echo QAVIA_BLOCKED",
		runner.Limits{Timeout: 30 * time.Second}, runner.Egress{AllowedHosts: c.allowed}))

	c.assert("metadata endpoint unreachable with a target allowed", problem,
		strings.Contains(result.Logs, "QAVIA_BLOCKED"),
		"a run with a target allowlist read the metadata endpoint: "+excerpt(result.Logs))
}

// wallClockKills is BE-4.5: the driver's timeout, not the framework's.
func (c *checker) wallClockKills(ctx context.Context) {
	result, problem := c.probe(ctx, "wall-clock", c.shell(
		"echo QAVIA_STARTED; while true; do :; done",
		runner.Limits{Timeout: 8 * time.Second, CPUs: 0.5}, runner.Egress{}))

	c.assert("an infinite loop is killed by the wall clock", problem,
		result.TimedOut && result.Reason() == "killed: wall clock limit reached",
		fmt.Sprintf("an infinite loop was not attributed to the wall clock: timedOut=%v reason=%q",
			result.TimedOut, result.Reason()))
}

// memoryLimitKills checks the OOM path is reported as a memory kill rather than as
// exit 137, which is the difference between an answer and a riddle.
func (c *checker) memoryLimitKills(ctx context.Context) {
	result, problem := c.probe(ctx, "memory", c.shell(
		// Grows a string until the cgroup limit stops it.
		"echo QAVIA_STARTED; awk 'BEGIN { s = \"\"; while (1) { s = s sprintf(\"%1000000s\", \"x\") } }'",
		runner.Limits{Timeout: 60 * time.Second, MemoryMiB: 64}, runner.Egress{}))

	c.assert("a memory hog is killed and attributed", problem,
		result.OOMKilled || strings.Contains(result.Reason(), "memory"),
		fmt.Sprintf("a memory hog was not reported as a memory kill: oom=%v exit=%d reason=%q",
			result.OOMKilled, result.ExitCode, result.Reason()))
}

// processLimitHolds checks the pids cgroup, which is what stops a fork bomb in the
// kernel rather than in the framework.
func (c *checker) processLimitHolds(ctx context.Context) {
	const limit = 48
	result, problem := c.probe(ctx, "pids", c.shell(
		// Each iteration tries to fork. The limit is reached long before 400.
		"i=0; while [ $i -lt 400 ]; do sh -c 'sleep 20' 2>/dev/null & i=$((i+1)); done; "+
			"echo QAVIA_FORKED=$(ps -o pid | wc -l)",
		runner.Limits{Timeout: 45 * time.Second, PIDs: limit}, runner.Egress{}))

	// The probe either cannot fork past the cap or is killed trying. Both are the
	// limit holding; what would fail is 400 live processes.
	held := result.ExitCode != 0 || result.TimedOut ||
		strings.Contains(result.Logs, "can't fork") ||
		strings.Contains(result.Logs, "Resource temporarily unavailable") ||
		countedUnder(result.Logs, limit+8)

	c.assert("a fork bomb is capped by the process limit", problem, held,
		"a fork bomb was not capped: "+excerpt(result.Logs))
}

// nothingLeftBehind is the one-shot rule. A container that outlives its run is state
// the next run could reach.
func (c *checker) nothingLeftBehind(ctx context.Context) {
	_, problem := c.probe(ctx, "reaping", c.shell("echo QAVIA_DONE",
		runner.Limits{Timeout: 20 * time.Second}, runner.Egress{}))

	// The sweeper reports how many orphans it found. After a clean run it must find
	// none, because the driver's own defer already removed the container.
	swept, err := c.driver.Sweep(ctx, 0)
	if err != nil {
		c.assert("nothing is left behind", fmt.Sprintf("the sweeper failed: %v", err), false, "")
		return
	}

	c.assert("nothing is left behind", problem, swept == 0,
		fmt.Sprintf("the sweeper found %d container(s) or network(s) a run should have removed itself",
			swept))
}

func (c *checker) assert(name, problem string, ok bool, failure string) {
	switch {
	case problem != "":
		c.failed++
		fmt.Printf("FAIL  %s\n      %s\n", name, problem)
	case ok:
		c.passed++
		fmt.Printf("PASS  %s\n", name)
	default:
		c.failed++
		fmt.Printf("FAIL  %s\n      %s\n", name, failure)
	}
}

func (c *checker) skip(name, why string) {
	c.skipped++
	fmt.Printf("SKIP  %s\n      %s\n", name, why)
}

func parseAllow(raw string) ([]runner.HostAddress, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var allowed []runner.HostAddress
	for _, pair := range strings.Split(raw, ",") {
		host, ip, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found || host == "" || ip == "" {
			return nil, fmt.Errorf("-allow wants host=ip pairs, got %q", pair)
		}
		allowed = append(allowed, runner.HostAddress{Host: host, IP: ip})
	}
	return allowed, nil
}

// countedUnder reads the fork count the pids probe printed, if it got that far.
func countedUnder(logs string, ceiling int) bool {
	_, after, found := strings.Cut(logs, "QAVIA_FORKED=")
	if !found {
		return false
	}
	count := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(after), "%d", &count); err != nil {
		return false
	}
	return count > 0 && count < ceiling
}

func excerpt(logs string) string {
	trimmed := strings.TrimSpace(logs)
	if len(trimmed) > 300 {
		return trimmed[:300] + "…"
	}
	if trimmed == "" {
		return "(no output)"
	}
	return trimmed
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "runnercheck: "+format+"\n", args...)
	os.Exit(2)
}
