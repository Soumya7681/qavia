// Package reports renders a project's state as a document somebody can send
// (F-12.5).
//
// The audience is the point. A dashboard is for the person doing the work; a report
// is for the person who asked whether the work is going well, and that person wants
// one file: what ran, what it covered, what failed and why, and what is being done
// about it.
//
// Three decisions shape the package:
//
//   - **Generated in a job.** A project with 400 tests and 40 analyses is not a
//     request that should be held open, so the row is created, the work is queued, and
//     the file is downloaded when it is ready (BE-5.9.1).
//   - **HTML is the source, PDF is a rendering of it.** One template, so the two
//     formats cannot disagree about what the report says.
//   - **The PDF is rendered in the runner image that already has a browser**, not in
//     the API process. Adding a headless browser to the API to print a document would
//     put a browser in the process that serves requests (BE-5.9.3).
package reports

import (
	"time"

	"github.com/google/uuid"
)

// Format is what a report is rendered as.
type Format string

const (
	FormatHTML Format = "html"
	FormatPDF  Format = "pdf"
)

func (f Format) Valid() bool { return f == FormatHTML || f == FormatPDF }

// Extension is the filename suffix, so a download arrives named something a browser
// and an email client both understand.
func (f Format) Extension() string {
	if f == FormatPDF {
		return "pdf"
	}
	return "html"
}

// ContentType is what the download is served as.
func (f Format) ContentType() string {
	if f == FormatPDF {
		return "application/pdf"
	}
	return "text/html; charset=utf-8"
}

// Status is where a report is in its life.
type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
)

// Report is one generated document.
type Report struct {
	ID        uuid.UUID
	ProjectID uuid.UUID

	Format Format
	Status Status

	// WindowDays is how far back the report looks. Recorded so two reports of the
	// same project are distinguishable by what they contain, not only by when they
	// were made.
	WindowDays int

	StorageKey string
	SizeBytes  int64

	Error string

	JobID       *uuid.UUID
	RequestedBy *uuid.UUID

	CreatedAt  time.Time
	FinishedAt *time.Time
}

// Ready reports whether there is a file to download.
func (r Report) Ready() bool { return r.Status == StatusReady && r.StorageKey != "" }

// Contents is everything a report renders, gathered before any HTML is written.
//
// A struct rather than a set of queries called from the template: the template is
// then a pure function of this, which is what makes the same data render identically
// as HTML and as PDF.
type Contents struct {
	ProjectName string
	GeneratedAt time.Time
	WindowDays  int

	Summary  Summary
	Runs     []RunLine
	Coverage Coverage
	Failures []Failure
	Defects  []DefectLine
}

// Summary is the headline: what a reader takes away without scrolling.
type Summary struct {
	Runs       int
	Passed     int
	Failed     int
	Flaky      int
	Skipped    int
	PassRate   float64
	LastRunAt  *time.Time
	OpenDefect int
}

// RunLine is one run in the history table.
type RunLine struct {
	ID       uuid.UUID
	At       time.Time
	Status   string
	Total    int
	Passed   int
	Failed   int
	Flaky    int
	Duration time.Duration
	Target   string
}

// Coverage is the requirement side: what was asked for against what was tested.
type Coverage struct {
	Requirements  int
	Covered       int
	ApprovedCases int
	TotalCases    int
	TestFiles     int
}

// Failure is one failing test with whatever explanation exists.
//
// The analysis is optional on purpose: a report that omitted unexplained failures
// would be a report that looks better than the project is.
type Failure struct {
	TestName string
	Status   string
	Message  string

	Analysed     bool
	Reason       string
	RootCause    string
	SuggestedFix string
	Evidence     []string
	Stability    *float64

	DefectID *uuid.UUID
}

// DefectLine is one defect in the tracker table.
type DefectLine struct {
	ID          uuid.UUID
	Title       string
	Severity    string
	Status      string
	Occurrences int
	CreatedAt   time.Time
}
