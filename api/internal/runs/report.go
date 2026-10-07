package runs

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The report contract (BE-4.9.2).
//
// Every runner image writes the same file, `.qavia/report.json`, in the same shape,
// whatever framework it wrapped. That is deliberate: the conversion lives in the
// image, next to the framework whose output it knows, so adding a framework never
// changes this package. Go reads one schema.
//
//	{
//	  "schema": "qavia.run/1",
//	  "framework": "node",
//	  "results": [
//	    {"name": "...", "file": "...", "status": "passed|failed|skipped",
//	     "durationMs": 12, "attempt": 1, "failureMessage": "..."}
//	  ]
//	}
//
// A report that does not parse is a failed run, not an empty one. An empty report
// with a zero exit is also a failed run: a green result with no tests is the most
// misleading outcome this platform could produce.

// ReportPath is where every image writes its report, relative to the workspace.
const ReportPath = ".qavia/report.json"

// Report is a run's normalised output.
type Report struct {
	Schema    string       `json:"schema"`
	Framework string       `json:"framework"`
	StartedAt string       `json:"startedAt,omitempty"`
	Results   []ReportItem `json:"results"`

	// Metrics is present for a performance run and absent for a functional one. A load
	// script has thresholds, which become results, and it also measures latency and
	// throughput — numbers no functional run produces, and the reason a performance run
	// is a different thing to look at (BE-9.1.3).
	Metrics *Metrics `json:"metrics,omitempty"`

	// Findings is present for a security run: one per probe that its detection rule
	// matched. Carried raw here and stored against the probe's result row, so a finding
	// is a failed result with security detail attached (BE-9.4).
	Findings []ReportFinding `json:"findings,omitempty"`
}

// ReportFinding is a security probe that succeeded, as the image reported it.
type ReportFinding struct {
	PayloadID    string `json:"payloadId"`
	Category     string `json:"category"`
	Endpoint     string `json:"endpoint"`
	Parameter    string `json:"parameter,omitempty"`
	Severity     string `json:"severity"`
	Evidence     string `json:"evidence"`
	Reproduction string `json:"reproduction"`
}

// Metrics is what a k6 run measured, normalised out of k6's own summary in the image so
// this package reads one shape whatever the load tool was.
type Metrics struct {
	// Requests is how many were sent, and Throughput is requests per second over the
	// run: the two numbers a load test exists to produce.
	Requests   int64   `json:"requests"`
	Throughput float64 `json:"throughput"`

	// ErrorRate is the share of requests that failed, between 0 and 1.
	ErrorRate float64 `json:"errorRate"`

	// Latency percentiles in milliseconds. A load test reported as an average is a load
	// test that hid its tail, and the tail is where the users are (F-11.2).
	LatencyAvgMs float64 `json:"latencyAvgMs"`
	LatencyP50Ms float64 `json:"latencyP50Ms"`
	LatencyP90Ms float64 `json:"latencyP90Ms"`
	LatencyP95Ms float64 `json:"latencyP95Ms"`
	LatencyP99Ms float64 `json:"latencyP99Ms"`
	LatencyMaxMs float64 `json:"latencyMaxMs"`

	// VirtualUsers is the peak concurrency the profile reached, carried so a series can
	// be read against the load that produced it.
	VirtualUsers int `json:"virtualUsers"`

	// DurationMs is how long the load phase ran.
	DurationMs int64 `json:"durationMs"`
}

// ReportItem is one test's outcome as the image reported it.
type ReportItem struct {
	Name           string `json:"name"`
	File           string `json:"file"`
	Status         string `json:"status"`
	DurationMs     int64  `json:"durationMs"`
	Attempt        int    `json:"attempt,omitempty"`
	FailureMessage string `json:"failureMessage,omitempty"`
}

// ParseReport decodes a run report and refuses anything it cannot trust.
func ParseReport(raw []byte) (Report, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Report{}, fmt.Errorf("the runner wrote no report, so the suite did not run")
	}

	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return Report{}, fmt.Errorf("the runner's report could not be read: %w", err)
	}

	// The version is checked rather than assumed: an image built against a later
	// schema must fail loudly here instead of producing results that look right.
	if report.Schema != "qavia.run/1" {
		return Report{}, fmt.Errorf("the runner reported schema %q, and this platform reads qavia.run/1",
			report.Schema)
	}

	return report, nil
}

// resultStatus maps a reported status onto the platform's enum.
//
// Anything unrecognised becomes an error rather than a pass. A status this platform
// does not know is a fact it cannot interpret, and interpreting it as success is
// the one choice that hides a problem.
func resultStatus(reported string) ResultStatus {
	switch strings.ToLower(strings.TrimSpace(reported)) {
	case "passed", "pass", "ok":
		return ResultPassed
	case "failed", "fail":
		return ResultFailed
	case "skipped", "skip", "pending", "todo":
		return ResultSkipped
	default:
		return ResultError
	}
}
