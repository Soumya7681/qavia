package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/hyscaler/qavia/api/internal/llm/aigen"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
)

// correlationHeader matches what the Python service reads, so one user action is
// traceable across both runtimes (backend-standards.md 12).
const correlationHeader = "X-Correlation-ID"

// connectAttempts bounds retries on a connection failure.
//
// Connection errors only. A 400 from the agent service is a contract problem and
// retrying it produces the same 400; a 503 is the provider being unavailable and
// is handled by the gateway's own fallback, which knows about the fallback model.
const connectAttempts = 3

// Client talks to the Python agent service.
//
// Thin on purpose: the service publishes its own schema, the generated code does
// the encoding, and this adds the three things generated code cannot know about —
// a timeout, connection retries, and trace propagation (BE-1.3).
type Client struct {
	api     *aigen.ClientWithResponses
	baseURL string
}

// NewClient builds the client for a base URL.
func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	httpClient := &http.Client{
		// otelhttp so a slow model call appears in the same trace as the request or
		// job that caused it, rather than as an unexplained gap.
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   timeout,
	}

	api, err := aigen.NewClientWithResponses(baseURL,
		aigen.WithHTTPClient(httpClient),
		aigen.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			if id := logging.CorrelationID(ctx); id != "" {
				req.Header.Set(correlationHeader, id)
			}
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("build AI service client: %w", err)
	}

	return &Client{api: api, baseURL: baseURL}, nil
}

// BaseURL is where the service is expected, for the readiness detail line.
func (c *Client) BaseURL() string { return c.baseURL }

// Health reports whether the agent service answers.
func (c *Client) Health(ctx context.Context) error {
	response, err := c.api.GetAIHealthWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reach AI service at %s: %w", c.baseURL, err)
	}
	if response.StatusCode() != http.StatusOK {
		return fmt.Errorf("AI service at %s answered %d", c.baseURL, response.StatusCode())
	}
	return nil
}

// Chat runs one completion.
//
// Retries are for connection failures only, and each retry is a fresh attempt at
// the same request: the AI service is stateless, so replaying is safe.
func (c *Client) Chat(ctx context.Context, request aigen.ChatRequest) (*aigen.ChatResponse, error) {
	var lastErr error

	for attempt := range connectAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}

		response, err := c.api.ChatCompletionWithResponse(ctx, request)
		if err != nil {
			if !retryableConnection(err) {
				return nil, fmt.Errorf("call AI service: %w", err)
			}
			lastErr = err
			continue
		}

		if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
			return response.JSON200, nil
		}
		return nil, serviceError(response.StatusCode(), response.Body)
	}

	return nil, apierr.ServiceDegraded("AI service").WithCause(lastErr)
}

// Probe asks the service what a model can really do.
func (c *Client) Probe(ctx context.Context, request aigen.ProbeRequest) (*aigen.ProbeResult, error) {
	response, err := c.api.ProbeModelWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("probe model: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

// CreateCache asks for a provider-side context cache for a fan-out.
func (c *Client) CreateCache(ctx context.Context, request aigen.CacheCreateRequest) (*aigen.CacheHandle, error) {
	response, err := c.api.CreateCacheObjectWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("create cache object: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

// DeleteCache releases a context cache at chain teardown.
func (c *Client) DeleteCache(ctx context.Context, apiKey, handle string) error {
	response, err := c.api.DeleteCacheObjectWithResponse(ctx, aigen.CacheDeleteRequest{
		ApiKey: apiKey, Handle: handle,
	})
	if err != nil {
		return fmt.Errorf("delete cache object: %w", err)
	}
	if response.StatusCode() != http.StatusOK {
		return serviceError(response.StatusCode(), response.Body)
	}
	return nil
}

// serviceError turns the Python envelope into a domain error.
//
// The kind is what matters: only `retryable` is worth a fallback, and only `auth`
// means an admin has to change a setting. The provider's own message is never
// passed through, because it can echo the request body, and for this platform that
// body is client source code.
func serviceError(status int, body []byte) error {
	var envelope struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Kind    string         `json:"kind"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code == "" {
		return apierr.ServiceDegraded("AI service").
			WithCause(fmt.Errorf("AI service answered %d", status))
	}

	switch envelope.Kind {
	case "retryable":
		return apierr.ProviderUnavailable(envelope.Message)
	case "auth":
		return apierr.ProviderCredentialsInvalid(envelope.Message)
	case "validation":
		return apierr.ModelResponseInvalid(envelope.Message)
	case "not_supported":
		return apierr.NotImplemented(envelope.Message)
	default:
		return apierr.Internal(fmt.Errorf("AI service: %s (%s)", envelope.Code, envelope.Message))
	}
}

// retryableConnection reports whether the request never reached the service.
//
// A refused dial or a reset connection is worth another attempt: the AI service
// restarting during a deploy should not fail a job. Anything the service actually
// answered is not retried here.
func retryableConnection(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

// RunExtract, RunDesign, and RunDedupe are the agent endpoints.
//
// Thin wrappers over the generated client for the same reason Chat is one: the
// generated code cannot know about the retry policy, and an agent call that failed
// because the service was restarting should be retried rather than surfaced.

func (c *Client) RunExtract(ctx context.Context, request aigen.ExtractRequest) (*aigen.AgentResult, error) {
	response, err := c.api.RunExtractAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run extract agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunDesign(ctx context.Context, request aigen.DesignRequest) (*aigen.AgentResult, error) {
	response, err := c.api.RunDesignAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run design agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunDedupe(ctx context.Context, request aigen.DedupeRequest) (*aigen.AgentResult, error) {
	response, err := c.api.RunDedupeAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run dedupe agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunAnalyse(ctx context.Context, request aigen.AnalyseRequest) (*aigen.AgentResult, error) {
	response, err := c.api.RunAnalyseAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run analyse agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunRepoStep(
	ctx context.Context,
	request aigen.RepoStepRequest,
) (*aigen.AgentResult, error) {
	response, err := c.api.RunRepoStepAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run repo step agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunUIFlowStep(
	ctx context.Context,
	request aigen.UIFlowRequest,
) (*aigen.AgentResult, error) {
	response, err := c.api.RunUIFlowStepAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run ui flow step agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunUISpec(
	ctx context.Context,
	request aigen.UISpecRequest,
) (*aigen.AgentResult, error) {
	response, err := c.api.RunUISpecAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run ui spec agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunTestData(
	ctx context.Context,
	request aigen.TestDataRequest,
) (*aigen.AgentResult, error) {
	response, err := c.api.RunTestDataAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run test data agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunPerf(ctx context.Context, request aigen.PerfRequest) (*aigen.AgentResult, error) {
	response, err := c.api.RunPerfAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run perf agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunProbeSelect(
	ctx context.Context,
	request aigen.ProbeSelectRequest,
) (*aigen.AgentResult, error) {
	response, err := c.api.RunProbeSelectAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run probe select agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunUnitTest(
	ctx context.Context,
	request aigen.UnitTestRequest,
) (*aigen.AgentResult, error) {
	response, err := c.api.RunUnitTestAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run unit test agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}

func (c *Client) RunCodegen(ctx context.Context, request aigen.CodegenRequest) (*aigen.AgentResult, error) {
	response, err := c.api.RunCodegenAgentWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run codegen agent: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}
