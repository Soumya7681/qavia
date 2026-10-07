package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hyscaler/qavia/api/internal/llm/aigen"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// Agent names, recorded on the llm_calls row so spend is attributable per agent
// rather than to "the platform".
const (
	AgentExtract  = "extract"
	AgentDesign   = "design"
	AgentDedupe   = "dedupe"
	AgentCodegen  = "codegen"
	AgentAnalyse  = "analyse"
	AgentRepo     = "repo"
	AgentUnitTest = "unittest"
	AgentUIFlow   = "uiflow"
	AgentUISpec   = "uispec"
	AgentTestData = "testdata"
	AgentPerf     = "perf"
	AgentProbe    = "probeselect"
)

// AgentCall is what every agent invocation needs from the caller.
//
// It names a tier through the agent rather than carrying one: which tier an agent
// runs on is the agent's decision, declared in Python beside its prompt, and the
// resolution to a model is this package's.
type AgentCall struct {
	ProjectID *uuid.UUID
	JobID     *uuid.UUID
}

// AgentResult is one agent's output plus what it cost.
type AgentResult struct {
	// Raw is the agent's structured result, still as JSON so the calling package
	// decodes into its own named type rather than passing a map around
	// (backend-standards.md 9).
	Raw json.RawMessage

	Usage Usage
	Cost  decimal.Decimal

	ProviderKind string
	ModelName    string
	FallbackUsed bool

	LatencyMS       int
	ValidationRetry int
}

// Extract asks what a parsed specification requires (F-4.1).
//
// The document is the normalized model, never the raw file: deterministic code
// already parsed it, and asking a model to re-read JSON structure costs money to
// be occasionally wrong (ai-architecture.md 2).
func (g *Gateway) Extract(
	ctx context.Context,
	call AgentCall,
	document any,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierReasoning, call)
	if err != nil {
		return AgentResult{}, err
	}

	encoded, err := asMap(document)
	if err != nil {
		return AgentResult{}, err
	}

	request := aigen.ExtractRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Document:             encoded,
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunExtract(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentExtract, TierReasoning, call, prepared, response), nil
}

// Design produces test cases for one requirement (F-5.1).
//
// One call per requirement, sharing the specification as a cached prefix. Bounding
// how many run at once is the caller's job: a 400-endpoint specification must not
// launch 400 concurrent provider calls (BE-2.7).
func (g *Gateway) Design(
	ctx context.Context,
	call AgentCall,
	document any,
	requirement any,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierReasoning, call)
	if err != nil {
		return AgentResult{}, err
	}

	encodedDocument, err := asMap(document)
	if err != nil {
		return AgentResult{}, err
	}
	encodedRequirement, err := asMap(requirement)
	if err != nil {
		return AgentResult{}, err
	}

	request := aigen.DesignRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Document:             encodedDocument,
		Requirement:          encodedRequirement,
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunDesign(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentDesign, TierReasoning, call, prepared, response), nil
}

// Dedupe decides which candidates duplicate something already stored (F-5.4).
//
// The cheap tier, and only for near misses: exact duplicates were caught by a hash
// before this was called, for free.
func (g *Gateway) Dedupe(
	ctx context.Context,
	call AgentCall,
	existing []any,
	candidates []any,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCheap, call)
	if err != nil {
		return AgentResult{}, err
	}

	encodedExisting, err := asMaps(existing)
	if err != nil {
		return AgentResult{}, err
	}
	encodedCandidates, err := asMaps(candidates)
	if err != nil {
		return AgentResult{}, err
	}

	request := aigen.DedupeRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Existing:             &encodedExisting,
		Candidates:           &encodedCandidates,
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunDedupe(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentDedupe, TierCheap, call, prepared, response), nil
}

// AnalyseInput is one failure and the material an explanation may cite.
//
// Context is separated from the failure's own details on purpose: it is the cacheable
// prefix, and a run with forty failures analyses forty times against the same log. At
// full price that is the difference between this feature being affordable and not
// (ai-architecture.md 3.6).
type AnalyseInput struct {
	Context map[string]any

	TestName       string
	Status         string
	Attempt        int
	FailureMessage string

	History []map[string]any

	// Feedback carries the evidence findings from a rejected attempt, so the retry is
	// a correction rather than another roll (BE-5.3.3).
	Feedback string
}

// Analyse explains one failure, on the reasoning tier.
//
// The hardest call the platform makes: read a log, a response, and a test, and say
// which of the three is wrong. It is also the one whose output is least trustworthy
// on its own, which is why every citation it returns is checked against the material
// before anything is stored (BE-5.3).
func (g *Gateway) Analyse(
	ctx context.Context,
	call AgentCall,
	input AnalyseInput,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierReasoning, call)
	if err != nil {
		return AgentResult{}, err
	}

	context, err := asMap(input.Context)
	if err != nil {
		return AgentResult{}, err
	}

	attempt := input.Attempt
	if attempt < 1 {
		attempt = 1
	}

	request := aigen.AnalyseRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Context:              &context,
		TestName:             &input.TestName,
		Status:               &input.Status,
		Attempt:              &attempt,
		FailureMessage:       &input.FailureMessage,
	}
	if len(input.History) > 0 {
		history := make([]map[string]any, 0, len(input.History))
		history = append(history, input.History...)
		request.History = &history
	}
	if input.Feedback != "" {
		request.Feedback = &input.Feedback
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunAnalyse(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentAnalyse, TierReasoning, call, prepared, response), nil
}

// RepoStepInput is one step of a repository exploration.
//
// Tree is the cacheable prefix: the file listing and the detected stack do not change
// between steps of one pass, so thirty steps are one cached prefix plus thirty short
// suffixes rather than thirty full prompts (ai-architecture.md 3.6).
type RepoStepInput struct {
	Tree  map[string]any
	Stack string

	Step   int
	Budget int

	// History is what has been looked at: {action, detail, result} per entry.
	History []map[string]any
}

// RepoStep asks what to look at next, on the code tier.
//
// One step per call, because the tools run in Go: the process that owns the checkout
// is the one that can safely validate a path, and the two processes do not share a
// filesystem (BE-6.4).
func (g *Gateway) RepoStep(
	ctx context.Context,
	call AgentCall,
	input RepoStepInput,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCode, call)
	if err != nil {
		return AgentResult{}, err
	}

	tree, err := asMap(input.Tree)
	if err != nil {
		return AgentResult{}, err
	}

	request := aigen.RepoStepRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Tree:                 &tree,
		Stack:                &input.Stack,
		Step:                 &input.Step,
		Budget:               &input.Budget,
	}
	if len(input.History) > 0 {
		history := make([]map[string]any, 0, len(input.History))
		history = append(history, input.History...)
		request.History = &history
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunRepoStep(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentRepo, TierCode, call, prepared, response), nil
}

// UIFlowInput is one step of a UI flow discovery.
//
// Context is the cacheable prefix: the application under discovery, its target, and
// whatever the platform already knows about it do not change between steps, so forty
// steps are one cached prefix plus forty short suffixes (ai-architecture.md 3.6).
type UIFlowInput struct {
	Context map[string]any

	// AppURL is the application being explored, not the model being called: the
	// provider and model come from the tier resolution in prepareAgent.
	AppURL string

	// Auth is the project's configured mode, so the agent expects a login form rather
	// than spending steps discovering that it needs one. The credentials themselves
	// never travel here: the browser container substitutes them (BE-7.3.2).
	Auth string

	Step   int
	Budget int

	// History is what has been done and seen: {action, detail, result} per entry.
	History []map[string]any
}

// UIFlowStep asks what to do next in the browser, on the code tier.
//
// One step per call, because the browser lives in a worker's container: the process
// that owns the container is the one that can check an action against the vocabulary,
// hold the budget, and substitute a credential without showing it to a model (BE-7.2).
func (g *Gateway) UIFlowStep(
	ctx context.Context,
	call AgentCall,
	input UIFlowInput,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCode, call)
	if err != nil {
		return AgentResult{}, err
	}

	discovered, err := asMap(input.Context)
	if err != nil {
		return AgentResult{}, err
	}

	request := aigen.UIFlowRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Context:              &discovered,
		AppUrl:               &input.AppURL,
		Auth:                 &input.Auth,
		Step:                 &input.Step,
		Budget:               &input.Budget,
	}
	if len(input.History) > 0 {
		history := make([]map[string]any, 0, len(input.History))
		history = append(history, input.History...)
		request.History = &history
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunUIFlowStep(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentUIFlow, TierCode, call, prepared, response), nil
}

// UISpecInput is one discovered flow to turn into a spec.
//
// Graph is the cacheable prefix: the pages and what can be done on each do not change
// between the flows of one application, so a suite of twelve specs is one cached
// prefix plus twelve short instructions, and a correction after a rejected selector is
// a cache read rather than the whole graph again (ai-architecture.md 3.6).
type UISpecInput struct {
	Graph map[string]any
	Flow  map[string]any

	// Feedback carries the selector policy's findings, or the validator's, from a
	// rejected attempt.
	Feedback string
}

// UISpec writes one Playwright spec for one flow, on the code tier.
func (g *Gateway) UISpec(
	ctx context.Context,
	call AgentCall,
	input UISpecInput,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCode, call)
	if err != nil {
		return AgentResult{}, err
	}

	graph, err := asMap(input.Graph)
	if err != nil {
		return AgentResult{}, err
	}
	flow, err := asMap(input.Flow)
	if err != nil {
		return AgentResult{}, err
	}

	request := aigen.UISpecRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Graph:                &graph,
		Flow:                 &flow,
	}
	if input.Feedback != "" {
		request.Feedback = &input.Feedback
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunUISpec(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentUISpec, TierCode, call, prepared, response), nil
}

// TestDataInput is one batch of realistic values for named fields.
//
// One call for the whole batch, on the cheap tier, and only for the fields somebody
// opted in: the bulk of a generated set comes from a seeded faker, which is free,
// instant, and identical every run (BE-8.2, ai-architecture.md 2).
type TestDataInput struct {
	// Fields describe what to write: name, type, format, pattern, length, description.
	Fields []map[string]any

	Count  int
	Locale string

	// Context is the cacheable prefix: what the object is and which fields it has.
	Context map[string]any
}

// TestData asks for realistic values for a handful of fields, on the cheap tier.
func (g *Gateway) TestData(
	ctx context.Context,
	call AgentCall,
	input TestDataInput,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCheap, call)
	if err != nil {
		return AgentResult{}, err
	}

	described, err := asMap(input.Context)
	if err != nil {
		return AgentResult{}, err
	}

	fields := make([]map[string]any, 0, len(input.Fields))
	fields = append(fields, input.Fields...)

	request := aigen.TestDataRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Fields:               &fields,
		Count:                &input.Count,
		Locale:               &input.Locale,
		Context:              &described,
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunTestData(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentTestData, TierCheap, call, prepared, response), nil
}

// PerfInput is a load-script generation.
//
// Endpoints is the cacheable prefix; the profile is fixed by whoever authorised the
// test and passed through as text, because it is a person's decision about how hard to
// hit a server, not the model's (BE-9.1).
type PerfInput struct {
	Endpoints map[string]any
	Profile   string
	P95Ms     int
	ErrorRate float64
}

// Perf generates a k6 load script, on the code tier.
func (g *Gateway) Perf(ctx context.Context, call AgentCall, input PerfInput) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCode, call)
	if err != nil {
		return AgentResult{}, err
	}

	endpoints, err := asMap(input.Endpoints)
	if err != nil {
		return AgentResult{}, err
	}

	errorRate := float32(input.ErrorRate)
	request := aigen.PerfRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Endpoints:            &endpoints,
		Profile:              &input.Profile,
		P95Ms:                &input.P95Ms,
		ErrorRate:            &errorRate,
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunPerf(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentPerf, TierCode, call, prepared, response), nil
}

// ProbeSelectInput is a security-probe plan request.
//
// The catalogue carries payload IDs and purposes, never the strings: the strings live
// in a reviewed Go library, and the agent's job is to choose which reviewed entry to
// aim where (BE-9.3.2).
type ProbeSelectInput struct {
	Endpoints  map[string]any
	Catalogue  []map[string]any
	Categories []string
}

// ProbeSelect asks which endpoints and payloads to probe, on the code tier.
func (g *Gateway) ProbeSelect(
	ctx context.Context,
	call AgentCall,
	input ProbeSelectInput,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCode, call)
	if err != nil {
		return AgentResult{}, err
	}

	endpoints, err := asMap(input.Endpoints)
	if err != nil {
		return AgentResult{}, err
	}

	catalogue := make([]map[string]any, 0, len(input.Catalogue))
	catalogue = append(catalogue, input.Catalogue...)
	categories := make([]string, 0, len(input.Categories))
	categories = append(categories, input.Categories...)

	request := aigen.ProbeSelectRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Endpoints:            &endpoints,
		Catalogue:            &catalogue,
		Categories:           &categories,
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunProbeSelect(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentProbe, TierCode, call, prepared, response), nil
}

// UnitTestInput is one untested target.
//
// Source is the cacheable prefix: the target's own code and its neighbours. A
// regeneration after a validation failure reuses it, so a correction is a cache read
// plus a short instruction rather than the whole file again (ai-architecture.md 3.6).
type UnitTestInput struct {
	Source map[string]any

	// Framework comes from the repository's own manifest, not from the model: the
	// project already said what it uses (BE-6.3).
	Framework string

	File    string
	Symbols []string

	// Feedback carries the validator's findings from a rejected attempt.
	Feedback string
}

// UnitTest writes one test file for one untested target, on the code tier.
func (g *Gateway) UnitTest(
	ctx context.Context,
	call AgentCall,
	input UnitTestInput,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCode, call)
	if err != nil {
		return AgentResult{}, err
	}

	source, err := asMap(input.Source)
	if err != nil {
		return AgentResult{}, err
	}

	request := aigen.UnitTestRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Source:               &source,
		Framework:            &input.Framework,
		File:                 &input.File,
		Symbols:              &input.Symbols,
	}
	if input.Feedback != "" {
		request.Feedback = &input.Feedback
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunUnitTest(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentUnitTest, TierCode, call, prepared, response), nil
}

// preparedAgent is a resolved tier plus what the request needs from it.
type preparedAgent struct {
	resolution Resolution
	target     aigen.Target
	fallback   *aigen.Target
	retries    int
}

// prepareAgent resolves the tier, checks the budget, and decrypts credentials.
//
// The order matters and is the same as a direct call: refusing after the provider
// has already been paid is a report rather than a ceiling.
func (g *Gateway) prepareAgent(ctx context.Context, tier Tier, call AgentCall) (preparedAgent, error) {
	resolution, err := g.Resolve(ctx, tier, call.ProjectID)
	if err != nil {
		return preparedAgent{}, err
	}
	if err := g.checkCeiling(ctx, call.ProjectID, resolution.Provider.ID); err != nil {
		return preparedAgent{}, err
	}

	target, err := g.target(ctx, resolution.Provider, resolution.Model, resolution.Assignment.Effort)
	if err != nil {
		return preparedAgent{}, err
	}

	prepared := preparedAgent{resolution: resolution, target: target}

	if resolution.FallbackModel != nil {
		fallback, err := g.target(ctx,
			*resolution.FallbackProvider, *resolution.FallbackModel, resolution.Assignment.Effort)
		if err == nil {
			prepared.fallback = &fallback
		}
	}

	if prepared.retries, err = g.settings.Int(ctx, "ai.max_validation_retries", settings.Target{}); err != nil {
		return preparedAgent{}, err
	}
	return prepared, nil
}

// recordAgent turns the service's answer into a result and writes the row.
func (g *Gateway) recordAgent(
	ctx context.Context,
	agent string,
	tier Tier,
	call AgentCall,
	prepared preparedAgent,
	response *aigen.AgentResult,
) AgentResult {
	result := AgentResult{
		ProviderKind:    string(response.ServedBy.ProviderKind),
		ModelName:       response.ServedBy.ModelId,
		FallbackUsed:    valueOr(response.ServedBy.FallbackUsed, false),
		LatencyMS:       valueOr(response.LatencyMs, 0),
		ValidationRetry: valueOr(response.ValidationRetries, 0),
	}
	if response.Usage != nil {
		result.Usage = Usage{
			InputTokens:      valueOr(response.Usage.InputTokens, 0),
			OutputTokens:     valueOr(response.Usage.OutputTokens, 0),
			CacheReadTokens:  valueOr(response.Usage.CacheReadTokens, 0),
			CacheWriteTokens: valueOr(response.Usage.CacheWriteTokens, 0),
		}
	}
	if encoded, err := json.Marshal(response.Result); err == nil {
		result.Raw = encoded
	}

	billed := prepared.resolution.Model
	billedProvider := prepared.resolution.Provider
	if result.FallbackUsed && prepared.resolution.FallbackModel != nil {
		billed = *prepared.resolution.FallbackModel
		billedProvider = *prepared.resolution.FallbackProvider
	}
	result.Cost = Cost(billed, result.Usage)

	g.record(ctx, Request{
		Tier:      tier,
		Agent:     agent,
		ProjectID: call.ProjectID,
		JobID:     call.JobID,
	}, billedProvider, billed, Result{
		Usage:        result.Usage,
		Cost:         result.Cost,
		FallbackUsed: result.FallbackUsed,
		LatencyMS:    result.LatencyMS,
	})

	return result
}

// asMap renders a value as the map shape the contract carries.
//
// Through JSON rather than reflection, so a domain type's own json tags decide
// what the model sees: the field names in the prompt are the ones in the contract,
// and a renamed Go field cannot silently change a prompt.
func asMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("llm: encode agent input: %w", err)
	}

	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("llm: agent input is not an object: %w", err)
	}
	return out, nil
}

func asMaps(values []any) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		encoded, err := asMap(value)
		if err != nil {
			return nil, err
		}
		out = append(out, encoded)
	}
	return out, nil
}

// Codegen turns approved test cases into a runnable file (F-6.1).
//
// The `code` tier rather than `reasoning`: writing a test file from cases that
// already say what to assert is a different job from working out what to assert,
// and the tiers exist so an operator can price them differently.
// Codegen writes one endpoint's file.
//
// feedback carries the validator's findings from a previous attempt at the same
// file, so a retry corrects something stated rather than hoping for a better roll.
// It is appended to the instruction, never to the specification, so the cached
// prefix stays byte-identical across attempts (BE-1.9, BE-3.4.2).
func (g *Gateway) Codegen(
	ctx context.Context,
	call AgentCall,
	document any,
	framework string,
	endpoint string,
	suggestedPath string,
	cases []any,
	feedback string,
) (AgentResult, error) {
	prepared, err := g.prepareAgent(ctx, TierCode, call)
	if err != nil {
		return AgentResult{}, err
	}

	encodedDocument, err := asMap(document)
	if err != nil {
		return AgentResult{}, err
	}
	encodedCases, err := asMaps(cases)
	if err != nil {
		return AgentResult{}, err
	}

	request := aigen.CodegenRequest{
		Target:               prepared.target,
		MaxValidationRetries: &prepared.retries,
		Document:             encodedDocument,
		Framework:            &framework,
		Endpoint:             &endpoint,
		SuggestedPath:        &suggestedPath,
		Cases:                &encodedCases,
	}
	if feedback != "" {
		request.Feedback = &feedback
	}
	if prepared.fallback != nil {
		request.Fallback.Set(*prepared.fallback)
	}

	response, err := g.client.RunCodegen(ctx, request)
	if err != nil {
		return AgentResult{}, err
	}
	return g.recordAgent(ctx, AgentCodegen, TierCode, call, prepared, response), nil
}
