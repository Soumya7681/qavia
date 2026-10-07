package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/testcases"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// TypeCodegen is the code generation stage.
const TypeCodegen = jobs.TypeGenerateCode

// generatedFile is the codegen agent's envelope.
//
// Schema-locked while the content is not: Go needs the path and the covered case
// IDs to store and trace the file, and neither can be recovered reliably from
// prose around a code block (BE-3.2).
type generatedFile struct {
	Path           string   `json:"path"`
	Content        string   `json:"content"`
	CoveredCaseIDs []string `json:"covered_case_ids"`
	Notes          string   `json:"notes"`
}

// CodegenHandler writes one file per endpoint from that endpoint's approved cases.
//
// Per endpoint rather than one call for the whole suite (work.md 8, open item 2).
// The specification is a cached prefix either way, so batching would save no input
// tokens; what it would cost is the per-endpoint retry, and one bad file failing
// the whole suite is the outcome that matters here.
type CodegenHandler struct {
	deps  Deps
	files *testfiles.Service
}

func NewCodegenHandler(deps Deps, files *testfiles.Service) *CodegenHandler {
	return &CodegenHandler{deps: deps, files: files}
}

func (h *CodegenHandler) Type() string { return TypeCodegen }

func (h *CodegenHandler) IdempotencyKey(payload jobs.StagePayload) string {
	return fmt.Sprintf("%s:%s:%d", payload.Chain, payload.ParentJobID, payload.Stage)
}

// Validator checks generated files with the real toolchain, inside the runner image
// that owns it. Optional: a worker with no container runtime still generates, and
// says on each file that nobody has checked it yet (BE-3.4).
type Validator interface {
	Validate(
		ctx context.Context,
		projectID uuid.UUID,
		framework testfiles.Framework,
		files map[string]string,
	) (map[string][]runner.ValidationProblem, error)
}

func (h *CodegenHandler) Handle(ctx context.Context, payload jobs.StagePayload, jc jobs.JobContext) error {
	document, _, err := loadDocument(ctx, h.deps, payload)
	if err != nil {
		return err
	}

	approved, err := h.deps.TestCases.Approved(ctx, payload.ProjectID)
	if err != nil {
		return err
	}
	if len(approved) == 0 {
		// Generating from drafts somebody is still editing wastes the call, so this
		// stops with something a user can act on rather than writing a suite from
		// unreviewed cases (BE-3.2).
		return apierr.NoApprovedCases()
	}

	groups := groupByEndpoint(approved)
	jc.Event("Writing Supertest files for %d endpoints from %d approved cases",
		len(groups), len(approved))

	limit, err := h.deps.Settings.Int(ctx, "jobs.ai_fan_out_limit",
		settings.Target{ProjectID: &payload.ProjectID})
	if err != nil {
		return err
	}

	prompt := promptDocument(document)
	jobID := jc.JobID()

	var (
		mutex   sync.Mutex
		written int
		covered int
		failed  []string
		done    int

		// Kept so the whole batch is validated in one container: toolchain start-up
		// dominates, so forty files cost what one file costs (BE-3.4).
		generated = map[string]generatedRecord{}
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(limit)

	for _, endpointGroup := range groups {
		group.Go(func() error {
			result, err := h.deps.Gateway.Codegen(groupCtx, llm.AgentCall{
				ProjectID: &payload.ProjectID,
				JobID:     &jobID,
			}, prompt, string(testfiles.FrameworkSupertest),
				endpointGroup.Endpoint, suggestedPath(endpointGroup.Endpoint),
				forCodegen(endpointGroup.Cases), "")

			mutex.Lock()
			defer mutex.Unlock()

			done++
			jc.Progress(done * 100 / len(groups))

			if err != nil {
				if groupCtx.Err() != nil {
					return err
				}
				// One endpoint failing is recorded and skipped: the other files are
				// worth keeping, and this one is visible in the log for a re-run.
				failed = append(failed, endpointGroup.Endpoint)
				slog.WarnContext(ctx, "codegen failed for endpoint",
					"endpoint", endpointGroup.Endpoint, "error", err)
				return nil
			}

			file, err := h.store(groupCtx, payload.ProjectID, endpointGroup, result)
			if err != nil {
				failed = append(failed, endpointGroup.Endpoint)
				slog.WarnContext(ctx, "storing generated file failed",
					"endpoint", endpointGroup.Endpoint, "error", err)
				return nil
			}

			generated[file.Path] = generatedRecord{file: file, group: endpointGroup}
			written++
			covered += len(file.TestCaseIDs)
			jc.Event("Wrote %s covering %d cases", file.Path, len(file.TestCaseIDs))
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return err
	}

	jc.Event("Generated %d files covering %d of %d approved cases",
		written, covered, len(approved))
	if len(failed) > 0 {
		jc.Event("%d endpoints produced no file and can be re-run: %v", len(failed), failed)
	}

	if err := h.validate(ctx, payload, prompt, generated, jc); err != nil {
		return err
	}

	jc.Progress(100)
	return nil
}

// generatedRecord is a written file and the cases it came from, kept so a rejected
// file can be regenerated from the same input.
type generatedRecord struct {
	file  testfiles.File
	group endpointGroup
}

// validate compiles and scans what was just written, then gives each rejected file
// one corrected attempt (BE-3.4).
//
// The retry budget is one, deliberately. A model that cannot satisfy a stated
// finding on the second try is not going to satisfy it on the fifth, and the failing
// file is more useful to a reviewer than another three provider calls: it stays
// stored, carries the findings in its validation note, and is named in the job log.
func (h *CodegenHandler) validate(
	ctx context.Context,
	payload jobs.StagePayload,
	prompt any,
	generated map[string]generatedRecord,
	jc jobs.JobContext,
) error {
	if len(generated) == 0 {
		return nil
	}
	if h.deps.Validator == nil {
		// Said once rather than per file: a worker with no runtime is a deployment
		// choice, not a fact about any particular file.
		jc.Event("Static validation was skipped: this worker has no container runtime")
		return nil
	}

	files := make(map[string]string, len(generated))
	for path, record := range generated {
		files[path] = record.file.Content
	}

	problems, err := h.deps.Validator.Validate(
		ctx, payload.ProjectID, testfiles.FrameworkSupertest, files)
	if err != nil {
		// A validator that could not run is not a suite that failed validation. The
		// files stay, unmarked, and the reason is in the log.
		jc.Event("Static validation could not run: %v", err)
		slog.WarnContext(ctx, "static validation failed to run", "error", err)
		return nil
	}

	valid := 0
	for path, record := range generated {
		found := problems[path]
		if len(found) == 0 {
			valid++
			if err := h.files.MarkValidated(ctx, record.file.ID, ""); err != nil {
				slog.WarnContext(ctx, "record validation", "file", path, "error", err)
			}
			continue
		}

		jc.Event("%s was rejected by the validator: %s", path, summarise(found))

		// Counted as valid when the correction passed, because that is what a reader
		// wants to know: how much of this suite the toolchain accepts now, not how
		// much of it was right first time.
		if h.regenerate(ctx, payload, prompt, record, found, jc) {
			valid++
		}
	}

	jc.Event("%d of %d files pass the framework's own toolchain", valid, len(generated))
	return nil
}

// regenerate asks for the file once more with the findings attached, then validates
// the replacement. It reports whether the file ends up valid.
func (h *CodegenHandler) regenerate(
	ctx context.Context,
	payload jobs.StagePayload,
	prompt any,
	record generatedRecord,
	found []runner.ValidationProblem,
	jc jobs.JobContext,
) bool {
	jobID := jc.JobID()

	result, err := h.deps.Gateway.Codegen(ctx, llm.AgentCall{
		ProjectID: &payload.ProjectID,
		JobID:     &jobID,
	}, prompt, string(testfiles.FrameworkSupertest),
		record.group.Endpoint, record.file.Path,
		forCodegen(record.group.Cases), summarise(found))
	if err != nil {
		h.recordRejection(ctx, record.file, found,
			fmt.Sprintf("regeneration could not run: %v", err), jc)
		return false
	}

	replacement, err := h.store(ctx, payload.ProjectID, record.group, result)
	if err != nil {
		h.recordRejection(ctx, record.file, found,
			fmt.Sprintf("the corrected file could not be stored: %v", err), jc)
		return false
	}

	second, err := h.deps.Validator.Validate(ctx, payload.ProjectID,
		testfiles.FrameworkSupertest, map[string]string{replacement.Path: replacement.Content})
	if err != nil {
		jc.Event("%s was regenerated but could not be re-validated: %v", replacement.Path, err)
		return false
	}

	remaining := second[replacement.Path]
	if len(remaining) == 0 {
		jc.Event("%s passed validation after one correction", replacement.Path)
		if err := h.files.MarkValidated(ctx, replacement.ID, ""); err != nil {
			slog.WarnContext(ctx, "record validation", "file", replacement.Path, "error", err)
		}
		return true
	}

	// Twice is enough. The file is kept and marked, because a rejected file a
	// reviewer can read beats a silent gap in the suite (BE-3.4.3).
	h.recordRejection(ctx, replacement, remaining, "", jc)
	return false
}

// recordRejection stores the findings on the file and names it in the job log.
func (h *CodegenHandler) recordRejection(
	ctx context.Context,
	file testfiles.File,
	found []runner.ValidationProblem,
	extra string,
	jc jobs.JobContext,
) {
	note := "Rejected by static validation: " + summarise(found)
	if extra != "" {
		note += " (" + extra + ")"
	}

	jc.Event("%s still fails validation after a correction and needs a human: %s",
		file.Path, summarise(found))

	if err := h.files.MarkRejected(ctx, file.ID, note); err != nil {
		slog.WarnContext(ctx, "record validation rejection", "file", file.Path, "error", err)
	}
}

// summarise renders findings for a prompt or a log line, capped so one pathological
// file cannot fill either.
func summarise(found []runner.ValidationProblem) string {
	const maxLines = 12

	lines := make([]string, 0, min(len(found), maxLines))
	for index, problem := range found {
		if index == maxLines {
			lines = append(lines, fmt.Sprintf("… and %d more", len(found)-maxLines))
			break
		}
		lines = append(lines, "- "+problem.String())
	}
	return strings.Join(lines, "\n")
}

// store validates the envelope and writes the file.
//
// The covered IDs are filtered against what was actually offered: a model naming a
// case it was not given is either confused or hallucinating, and either way that ID
// must not end up in a coverage number.
func (h *CodegenHandler) store(
	ctx context.Context,
	projectID uuid.UUID,
	group endpointGroup,
	result llm.AgentResult,
) (testfiles.File, error) {
	var envelope generatedFile
	if err := json.Unmarshal(result.Raw, &envelope); err != nil {
		return testfiles.File{}, fmt.Errorf("decode generated file: %w", err)
	}

	offered := make(map[string]uuid.UUID, len(group.Cases))
	for _, item := range group.Cases {
		offered[item.ID.String()] = item.ID
	}

	covered := make([]uuid.UUID, 0, len(envelope.CoveredCaseIDs))
	for _, raw := range envelope.CoveredCaseIDs {
		if id, known := offered[strings.TrimSpace(raw)]; known {
			covered = append(covered, id)
		}
	}
	if len(covered) == 0 {
		// A file that covers nothing is not worth storing: it would show up in the
		// browser as a file and in coverage as nothing.
		return testfiles.File{}, fmt.Errorf(
			"the generated file claimed no cases from %s", group.Endpoint)
	}

	return h.files.Save(ctx, testfiles.SaveInput{
		ProjectID:   projectID,
		Framework:   testfiles.FrameworkSupertest,
		Path:        envelope.Path,
		Content:     envelope.Content,
		TestCaseIDs: covered,
		GeneratedBy: result.ModelName,
	})
}

// endpointGroup is one endpoint's approved cases.
type endpointGroup struct {
	Endpoint string
	Cases    []testcases.ApprovedCase
}

// groupByEndpoint puts one file's worth of cases together.
//
// Sorted, so two runs over the same suite produce files in the same order and the
// prompt prefix stays stable across the fan-out.
func groupByEndpoint(cases []testcases.ApprovedCase) []endpointGroup {
	byEndpoint := map[string][]testcases.ApprovedCase{}
	for _, item := range cases {
		endpoint := strings.TrimSpace(item.Method + " " + item.Path)
		if endpoint == "" {
			// The query already prefers the case's own endpoint over its
			// requirement's, so reaching here means neither knew: a case written by
			// hand with no endpoint given.
			// A case with no endpoint still belongs somewhere: dropping it would
			// silently lose approved work.
			endpoint = "general"
		}
		byEndpoint[endpoint] = append(byEndpoint[endpoint], item)
	}

	endpoints := make([]string, 0, len(byEndpoint))
	for endpoint := range byEndpoint {
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)

	groups := make([]endpointGroup, 0, len(endpoints))
	for _, endpoint := range endpoints {
		groups = append(groups, endpointGroup{Endpoint: endpoint, Cases: byEndpoint[endpoint]})
	}
	return groups
}

// suggestedPath turns "GET /tickets/{id}" into "tickets-id.get.test.ts".
//
// A suggestion rather than a rule: the model may choose otherwise, and whatever it
// chooses is validated before it is stored.
func suggestedPath(endpoint string) string {
	fields := strings.Fields(endpoint)
	method := "all"
	route := endpoint

	if len(fields) >= 2 {
		method = strings.ToLower(fields[0])
		route = fields[1]
	}

	var name strings.Builder
	for _, r := range route {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			name.WriteRune(r)
		case r == '/' || r == '{' || r == '}' || r == '-' || r == '_':
			if name.Len() > 0 && !strings.HasSuffix(name.String(), "-") {
				name.WriteRune('-')
			}
		}
	}

	slug := strings.Trim(strings.ToLower(name.String()), "-")
	if slug == "" {
		slug = "api"
	}
	return fmt.Sprintf("%s.%s.test.ts", slug, method)
}

// forCodegen renders cases as the agent's input: what to assert, and the ID to
// claim it by.
func forCodegen(cases []testcases.ApprovedCase) []any {
	out := make([]any, 0, len(cases))
	for _, item := range cases {
		steps := make([]string, 0, len(item.Steps))
		for _, step := range item.Steps {
			steps = append(steps, strings.TrimSpace(
				step.Action+" "+step.Data+" -> "+step.Expected))
		}

		out = append(out, map[string]any{
			"id":            item.ID.String(),
			"title":         item.Title,
			"preconditions": item.Preconditions,
			"steps":         strings.Join(steps, "; "),
			"expected":      item.Expected,
			"category":      string(item.Category),
		})
	}
	return out
}
