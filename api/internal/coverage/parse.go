// Package coverage measures how much of a repository its own tests execute
// (F-7.14, F-12.3).
//
// One rule shapes everything here: **the number comes from the project's own coverage
// tool.** Not from a model, and not from instrumentation this platform adds. A client
// comparing our figure against the one their CI prints has to see the same figure, and
// the only way to guarantee that is to run the tool they run and read what it wrote.
//
// Which means this package is mostly parsers. Each coverage tool writes its own
// format, and the honest way to support one is to read its native report rather than
// to ask it for a summary line and scrape a percentage out of prose.
//
// The second rule is separation: code coverage is never merged with requirement
// coverage (FR-7.2). Eighty per cent of requirements having a test case and eighty per
// cent of lines being executed are different facts, and averaging them produces a
// number that describes neither.
package coverage

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Report is a parsed coverage result.
type Report struct {
	// Tool is what produced it, recorded because "coverage is 74%" only means
	// something alongside "as measured by vitest run --coverage".
	Tool string

	LinesTotal      int
	LinesCovered    int
	BranchesTotal   int
	BranchesCovered int

	Files []File
}

// File is one file's coverage.
type File struct {
	Path string

	LinesTotal      int
	LinesCovered    int
	BranchesTotal   int
	BranchesCovered int
}

// LineRate is the proportion of lines executed, or -1 when the tool measured none.
//
// Minus one rather than zero, because a file with no executable lines — an interface,
// a type declaration, a barrel export — is not a file with no coverage, and showing it
// as 0% puts it at the top of a list of things to fix when there is nothing to fix.
func (f File) LineRate() float64 {
	if f.LinesTotal == 0 {
		return -1
	}
	return float64(f.LinesCovered) / float64(f.LinesTotal)
}

// LineRate is the same for the whole report.
func (r Report) LineRate() float64 {
	if r.LinesTotal == 0 {
		return -1
	}
	return float64(r.LinesCovered) / float64(r.LinesTotal)
}

// BranchRate is the proportion of branches taken, or -1 when the tool did not measure
// branches. Several do not, and reporting zero would claim every conditional is
// untested.
func (r Report) BranchRate() float64 {
	if r.BranchesTotal == 0 {
		return -1
	}
	return float64(r.BranchesCovered) / float64(r.BranchesTotal)
}

// Parse reads whichever format the report is in.
//
// The format is detected from the content rather than taken from a caller's claim: the
// tool wrote the file, and what it wrote is the ground truth about which parser to
// use.
func Parse(raw []byte) (Report, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return Report{}, fmt.Errorf("the coverage report is empty")
	}

	switch {
	case strings.HasPrefix(trimmed, "{"):
		return parseJSON([]byte(trimmed))
	case strings.HasPrefix(trimmed, "<"):
		return parseXML([]byte(trimmed))
	case strings.HasPrefix(trimmed, "TN:"), strings.HasPrefix(trimmed, "SF:"):
		return parseLCOV(trimmed)
	case strings.HasPrefix(trimmed, "mode:"):
		return parseGoProfile(trimmed)
	default:
		return Report{}, fmt.Errorf("the coverage report is in a format this platform does not read")
	}
}

// parseJSON reads Istanbul's coverage-final.json, which is what Jest, Vitest, nyc, and
// c8 all write.
//
// The shape is one entry per file with maps of statement and branch counts. Statements
// are used for the line figure rather than the `lines` map, because Istanbul's own
// summary does the same and a platform reporting a different number from the tool's own
// HTML would be indefensible.
func parseJSON(raw []byte) (Report, error) {
	var istanbul map[string]struct {
		Path         string           `json:"path"`
		StatementMap map[string]any   `json:"statementMap"`
		S            map[string]int   `json:"s"`
		BranchMap    map[string]any   `json:"branchMap"`
		B            map[string][]int `json:"b"`
	}
	if err := json.Unmarshal(raw, &istanbul); err != nil {
		return Report{}, fmt.Errorf("read the coverage report: %w", err)
	}
	if len(istanbul) == 0 {
		return Report{}, fmt.Errorf("the coverage report names no files")
	}

	report := Report{Tool: "istanbul"}

	for key, entry := range istanbul {
		name := entry.Path
		if name == "" {
			name = key
		}

		file := File{Path: name}
		for _, hits := range entry.S {
			file.LinesTotal++
			if hits > 0 {
				file.LinesCovered++
			}
		}
		for _, arms := range entry.B {
			for _, hits := range arms {
				file.BranchesTotal++
				if hits > 0 {
					file.BranchesCovered++
				}
			}
		}

		report.add(file)
	}

	report.finish()
	return report, nil
}

// parseXML reads Cobertura and JaCoCo, which cover pytest-cov, coverage.py, and the
// JVM tools.
func parseXML(raw []byte) (Report, error) {
	// Cobertura: <coverage><packages><package><classes><class filename=…><lines>
	var cobertura struct {
		XMLName  xml.Name `xml:"coverage"`
		Packages []struct {
			Classes []struct {
				Filename string `xml:"filename,attr"`
				Lines    []struct {
					Number int    `xml:"number,attr"`
					Hits   int    `xml:"hits,attr"`
					Branch string `xml:"branch,attr"`
					Rate   string `xml:"condition-coverage,attr"`
				} `xml:"lines>line"`
			} `xml:"classes>class"`
		} `xml:"packages>package"`
	}

	if err := xml.Unmarshal(raw, &cobertura); err == nil && len(cobertura.Packages) > 0 {
		report := Report{Tool: "cobertura"}

		for _, pkg := range cobertura.Packages {
			for _, class := range pkg.Classes {
				file := File{Path: class.Filename}
				for _, line := range class.Lines {
					file.LinesTotal++
					if line.Hits > 0 {
						file.LinesCovered++
					}
					if line.Branch == "true" {
						total, taken := parseConditionCoverage(line.Rate)
						file.BranchesTotal += total
						file.BranchesCovered += taken
					}
				}
				report.add(file)
			}
		}

		report.finish()
		return report, nil
	}

	// JaCoCo: <report><package name=…><sourcefile name=…><counter type="LINE" …>
	var jacoco struct {
		XMLName  xml.Name `xml:"report"`
		Packages []struct {
			Name        string `xml:"name,attr"`
			SourceFiles []struct {
				Name     string `xml:"name,attr"`
				Counters []struct {
					Type    string `xml:"type,attr"`
					Missed  int    `xml:"missed,attr"`
					Covered int    `xml:"covered,attr"`
				} `xml:"counter"`
			} `xml:"sourcefile"`
		} `xml:"package"`
	}
	if err := xml.Unmarshal(raw, &jacoco); err != nil || len(jacoco.Packages) == 0 {
		return Report{}, fmt.Errorf("the XML coverage report is neither Cobertura nor JaCoCo")
	}

	report := Report{Tool: "jacoco"}
	for _, pkg := range jacoco.Packages {
		for _, source := range pkg.SourceFiles {
			file := File{Path: path.Join(pkg.Name, source.Name)}
			for _, counter := range source.Counters {
				switch counter.Type {
				case "LINE":
					file.LinesTotal = counter.Missed + counter.Covered
					file.LinesCovered = counter.Covered
				case "BRANCH":
					file.BranchesTotal = counter.Missed + counter.Covered
					file.BranchesCovered = counter.Covered
				}
			}
			report.add(file)
		}
	}

	report.finish()
	return report, nil
}

// parseLCOV reads lcov.info, which most JavaScript tools write alongside their JSON.
func parseLCOV(text string) (Report, error) {
	report := Report{Tool: "lcov"}
	current := File{}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "SF:"):
			current = File{Path: strings.TrimPrefix(line, "SF:")}
		case strings.HasPrefix(line, "LF:"):
			current.LinesTotal = atoi(strings.TrimPrefix(line, "LF:"))
		case strings.HasPrefix(line, "LH:"):
			current.LinesCovered = atoi(strings.TrimPrefix(line, "LH:"))
		case strings.HasPrefix(line, "BRF:"):
			current.BranchesTotal = atoi(strings.TrimPrefix(line, "BRF:"))
		case strings.HasPrefix(line, "BRH:"):
			current.BranchesCovered = atoi(strings.TrimPrefix(line, "BRH:"))
		case line == "end_of_record":
			if current.Path != "" {
				report.add(current)
			}
			current = File{}
		}
	}

	if len(report.Files) == 0 {
		return Report{}, fmt.Errorf("the lcov report names no files")
	}

	report.finish()
	return report, nil
}

// parseGoProfile reads `go test -coverprofile`, whose format is statement blocks
// rather than lines.
//
// A block covers a range of lines and is counted once, which is what `go tool cover`
// itself reports. Counting the lines in each block instead would produce a number that
// disagrees with the toolchain, and disagreeing with the toolchain is the one thing
// this package must not do.
func parseGoProfile(text string) (Report, error) {
	report := Report{Tool: "go cover"}
	byFile := map[string]*File{}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}

		// name.go:from.col,to.col statements count
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		location := fields[0]
		statements := atoi(fields[1])
		count := atoi(fields[2])

		name := location
		if index := strings.LastIndex(location, ":"); index > 0 {
			name = location[:index]
		}

		file, seen := byFile[name]
		if !seen {
			file = &File{Path: name}
			byFile[name] = file
		}
		file.LinesTotal += statements
		if count > 0 {
			file.LinesCovered += statements
		}
	}

	if len(byFile) == 0 {
		return Report{}, fmt.Errorf("the coverage profile names no files")
	}

	for _, file := range byFile {
		report.add(*file)
	}
	report.finish()
	return report, nil
}

// add records one file and its totals.
func (r *Report) add(file File) {
	r.Files = append(r.Files, file)
	r.LinesTotal += file.LinesTotal
	r.LinesCovered += file.LinesCovered
	r.BranchesTotal += file.BranchesTotal
	r.BranchesCovered += file.BranchesCovered
}

// finish sorts the files least covered first, which is the order anybody reads them
// in, and normalises paths so a report from a container matches the repository.
func (r *Report) finish() {
	for index := range r.Files {
		r.Files[index].Path = normalisePath(r.Files[index].Path)
	}

	sort.SliceStable(r.Files, func(first, second int) bool {
		left, right := r.Files[first].LineRate(), r.Files[second].LineRate()
		switch {
		case left < 0 && right < 0:
			return r.Files[first].Path < r.Files[second].Path
		case left < 0:
			// Files with nothing to measure sort last: they are not gaps.
			return false
		case right < 0:
			return true
		case left != right:
			return left < right
		default:
			return r.Files[first].Path < r.Files[second].Path
		}
	})
}

// normalisePath turns a report's path into a repository-relative one.
//
// Coverage tools write absolute paths, and inside the runner the repository is at
// /workspace. A report full of /workspace/src/index.ts cannot be joined to anything a
// user sees.
func normalisePath(raw string) string {
	cleaned := path.Clean(strings.ReplaceAll(raw, "\\", "/"))
	cleaned = strings.TrimPrefix(cleaned, "/workspace/")
	cleaned = strings.TrimPrefix(cleaned, "workspace/")
	cleaned = strings.TrimPrefix(cleaned, "./")
	return strings.TrimPrefix(cleaned, "/")
}

// parseConditionCoverage reads Cobertura's `condition-coverage="50% (1/2)"`.
func parseConditionCoverage(raw string) (total, taken int) {
	open := strings.Index(raw, "(")
	slash := strings.Index(raw, "/")
	closing := strings.Index(raw, ")")
	if open < 0 || slash < open || closing < slash {
		return 0, 0
	}
	return atoi(raw[slash+1 : closing]), atoi(raw[open+1 : slash])
}

func atoi(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}
