package mocks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/datagen"
	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Endpoints is the slice of the ingest service this package needs.
type Endpoints interface {
	Endpoints(ctx context.Context, projectID uuid.UUID, artifactID *uuid.UUID) ([]ingest.Endpoint, error)
}

// Containers is the slice of the container driver this package needs.
//
// Three methods, because a mock has a lifecycle and a run does not: it starts, it is
// asked how it is doing, and it stops.
type Containers interface {
	StartService(ctx context.Context, spec runner.ServiceSpec) (*runner.Service, error)
	ServiceStatus(ctx context.Context, containerID string) (runner.ServiceStatus, error)
	StopService(ctx context.Context, containerID string)
	Available(ctx context.Context) bool
}

// Settings supplies the image, the limits, and the runtime, and is where the allowlist
// is written.
type Settings interface {
	String(ctx context.Context, key string, target settings.Target) (string, error)
	StringList(ctx context.Context, key string, target settings.Target) ([]string, error)
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	Float(ctx context.Context, key string, target settings.Target) (float64, error)
	Duration(ctx context.Context, key string, target settings.Target) (time.Duration, error)
	Bool(ctx context.Context, key string, target settings.Target) (bool, error)
}

// Allowlist adds the mock's own host to the project's allowed targets.
//
// Declared as the one write this package performs rather than taking the settings
// service whole: the mock's URL has to be reachable by a generated suite, and the
// allowlist is the platform's own gate in front of it (BE-8.6.5).
type Allowlist interface {
	Add(ctx context.Context, projectID uuid.UUID, host string, actor httpx.Principal) error
}

// Service starts, stops, and reports on a project's mock.
type Service struct {
	db         *store.DB
	endpoints  Endpoints
	containers Containers
	settings   Settings
	allowlist  Allowlist

	generator *datagen.Generator
}

func NewService(
	db *store.DB,
	endpoints Endpoints,
	containers Containers,
	settingsService Settings,
	allowlist Allowlist,
) *Service {
	return &Service{
		db:         db,
		endpoints:  endpoints,
		containers: containers,
		settings:   settingsService,
		allowlist:  allowlist,
		generator:  datagen.NewGenerator(),
	}
}

// StartInput is one request to bring a mock up.
type StartInput struct {
	ProjectID uuid.UUID
	Actor     httpx.Principal

	Faults Faults

	// Samples is how many response bodies to generate per endpoint, so a client listing
	// twice does not see identical data. Zero uses the default.
	Samples int

	// Seed makes the generated bodies reproducible, so a client-side test written
	// against the mock keeps passing for the right reason.
	Seed uint64
}

// Start brings a project's mock up.
//
// Idempotent in the way that matters: starting a mock that is already running replaces
// it, because the caller's intent is "serve this configuration" rather than "add a
// second container". The old container is stopped first, so a project never has two.
func (s *Service) Start(ctx context.Context, input StartInput) (Mock, error) {
	if s.containers == nil || !s.containers.Available(ctx) {
		return Mock{}, apierr.MockUnavailable(
			"No container runtime is available on this host.")
	}
	if err := input.Faults.Validate(); err != nil {
		return Mock{}, apierr.Validation(err.Error(), map[string]any{"field": "faults"})
	}

	routes, err := s.routesFor(ctx, input)
	if err != nil {
		return Mock{}, err
	}
	if len(routes) == 0 {
		return Mock{}, apierr.NoMockRoutes()
	}

	// Whatever was running is stopped before the new one starts, so a failed start does
	// not leave two containers and the row never points at the older of them.
	if existing, err := s.Get(ctx, input.ProjectID); err == nil && existing.ContainerID != "" {
		s.containers.StopService(ctx, existing.ContainerID)
	}

	image, err := s.settings.String(ctx, "mock.image", settings.Target{})
	if err != nil {
		return Mock{}, apierr.Internal(fmt.Errorf("read the mock image: %w", err))
	}
	if strings.TrimSpace(image) == "" {
		return Mock{}, apierr.MockUnavailable(
			"No mock server image is configured. Set mock.image to the built image reference.")
	}

	limits, err := s.limits(ctx, input.ProjectID)
	if err != nil {
		return Mock{}, err
	}
	runtime, err := s.runtime(ctx)
	if err != nil {
		return Mock{}, err
	}

	if input.Faults.Seed == 0 {
		// Derived from the generated-body seed rather than the clock, so one mock is one
		// reproducible thing: the same start produces the same bodies and the same
		// pattern of injected failures.
		input.Faults.Seed = input.Seed
	}

	config, err := json.Marshal(Config{
		Schema: ConfigSchema,
		Routes: routes,
		Faults: input.Faults,
	})
	if err != nil {
		return Mock{}, apierr.Internal(fmt.Errorf("encode the mock configuration: %w", err))
	}

	service, err := s.containers.StartService(ctx, runner.ServiceSpec{
		ID:      "mock-" + input.ProjectID.String(),
		Image:   image,
		Port:    containerPort,
		Config:  config,
		Limits:  limits,
		Runtime: runtime,
	})
	if err != nil {
		// Recorded as failed with the reason, rather than left as stopped: a button that
		// did nothing and a start that failed are different things to the person who
		// pressed it.
		s.recordFailure(ctx, input, routes, err)

		if errors.Is(err, runner.ErrUnavailable) {
			return Mock{}, apierr.MockUnavailable("The container runtime is not answering.")
		}
		return Mock{}, apierr.Internal(fmt.Errorf("start the mock server: %w", err))
	}

	host, err := s.reachableHost(ctx)
	if err != nil {
		s.containers.StopService(ctx, service.ContainerID)
		return Mock{}, err
	}
	mockURL := fmt.Sprintf("http://%s:%d", host, service.HostPort)

	stored, err := s.store(ctx, input, routes, service, mockURL, image)
	if err != nil {
		// The container is running and nothing recorded it, which is exactly the leak the
		// row exists to prevent.
		s.containers.StopService(ctx, service.ContainerID)
		return Mock{}, err
	}

	// The allowlist last, and best effort: a mock that is up and unreachable by a
	// generated suite is still a mock a client app can use, and refusing the whole start
	// over a settings write would be the worse trade (BE-8.6.5).
	if s.allowlist != nil {
		if err := s.allowlist.Add(ctx, input.ProjectID, host, input.Actor); err != nil {
			slog.WarnContext(ctx, "add the mock host to the project allowlist",
				"project_id", input.ProjectID, "host", host, "error", err)
		}
	}

	return stored, nil
}

// containerPort is the port the mock listens on inside its container. The host port is
// Docker's choice and is read back after the start.
const containerPort = 8080

// Stop takes a project's mock down.
//
// Safe to call twice: stopping a container that is already gone is a success, and the
// row is corrected either way.
func (s *Service) Stop(ctx context.Context, projectID uuid.UUID) (Mock, error) {
	mock, err := s.Get(ctx, projectID)
	if err != nil {
		return Mock{}, err
	}

	if mock.ContainerID != "" && s.containers != nil {
		s.containers.StopService(ctx, mock.ContainerID)
	}

	row, err := s.db.Queries().MarkMockStopped(ctx, dbgen.MarkMockStoppedParams{
		ProjectID: projectID, Error: "",
	})
	if err != nil {
		return Mock{}, apierr.Internal(fmt.Errorf("record the mock as stopped: %w", err))
	}
	return toMock(row), nil
}

// Get is a project's mock as the platform recorded it, reconciled against the host.
//
// The reconciliation is the point: a container that died takes its row's word for
// nothing, and a status panel that says running when nothing is listening is worse than
// one that says stopped.
func (s *Service) Get(ctx context.Context, projectID uuid.UUID) (Mock, error) {
	row, err := s.db.Queries().GetMockServer(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Mock{}, apierr.NoMockServer()
		}
		return Mock{}, apierr.Internal(fmt.Errorf("read the mock server: %w", err))
	}

	mock := toMock(row)
	if !mock.Running() || s.containers == nil {
		return mock, nil
	}

	status, err := s.containers.ServiceStatus(ctx, mock.ContainerID)
	if err != nil {
		// The runtime could not be asked. The row is returned as it stands rather than
		// rewritten on a failure to look: a Docker socket that blinked is not a mock that
		// stopped.
		slog.WarnContext(ctx, "check the mock container",
			"project_id", projectID, "error", err)
		return mock, nil
	}
	if status.Running {
		return mock, nil
	}

	// Gone. Recorded, so the next reader is not told the same untruth.
	corrected, err := s.db.Queries().MarkMockStopped(ctx, dbgen.MarkMockStoppedParams{
		ProjectID: projectID,
		Error:     fmt.Sprintf("the container exited with status %d", status.ExitCode),
	})
	if err != nil {
		return mock, nil //nolint:nilerr // The read succeeded; only the correction failed.
	}
	return toMock(corrected), nil
}

// routesFor builds the mock's routes from the project's specification.
func (s *Service) routesFor(ctx context.Context, input StartInput) ([]Route, error) {
	endpoints, err := s.endpoints.Endpoints(ctx, input.ProjectID, nil)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the project's endpoints: %w", err))
	}

	samples := input.Samples
	if samples <= 0 {
		samples = defaultSamples
	}
	if samples > maxSamples {
		samples = maxSamples
	}

	routes := make([]Route, 0, len(endpoints))
	for _, endpoint := range endpoints {
		route := Route{
			Key:    endpoint.Key(),
			Method: strings.ToUpper(endpoint.Method),
			Path:   endpoint.Path,
		}

		for _, response := range successResponses(endpoint) {
			status, err := strconv.Atoi(response.Status)
			if err != nil {
				status = 200
			}

			if !hasSchema(response.Schema) {
				// A documented response with no schema is still a route worth serving: a
				// 204 with an empty body is a real answer.
				route.Responses = append(route.Responses, Response{Status: status})
				continue
			}

			generated, err := s.generator.Generate(datagen.Request{
				Schema: response.Schema,
				Count:  samples,
				Seed:   input.Seed,
			})
			if err != nil {
				return nil, apierr.Internal(fmt.Errorf("generate a mock response: %w", err))
			}

			for _, record := range generated.Records {
				body, err := json.Marshal(shapeFor(response.Schema, record))
				if err != nil {
					return nil, apierr.Internal(fmt.Errorf("encode a mock response: %w", err))
				}
				route.Responses = append(route.Responses, Response{Status: status, Body: body})
			}
		}

		if len(route.Responses) == 0 {
			// Nothing documented at all. Served as a 200 with no body rather than omitted,
			// so a client app calling it gets an answer instead of the mock's 404.
			route.Responses = append(route.Responses, Response{Status: 200})
		}
		routes = append(routes, route)
	}

	return routes, nil
}

const (
	defaultSamples = 3
	maxSamples     = 20
)

// shapeFor wraps a generated record in the shape the response declares: a schema
// describing an array gets a list, and one describing an object gets the object.
func shapeFor(schema ingest.Schema, record datagen.Record) any {
	if schema.Type == "array" {
		// One element rather than a page: a mock returning forty invented rows makes a
		// client's list screen look right and its pagination untested.
		return []any{record.Map()}
	}
	return record.Map()
}

// successResponses are the ones worth serving: the documented successes, and the first
// documented response if none of them are.
func successResponses(endpoint ingest.Endpoint) []ingest.Response {
	var successes []ingest.Response
	for _, response := range endpoint.Responses {
		if strings.HasPrefix(response.Status, "2") {
			successes = append(successes, response)
		}
	}
	if len(successes) > 0 {
		return successes
	}
	if len(endpoint.Responses) > 0 {
		return endpoint.Responses[:1]
	}
	return nil
}

func hasSchema(schema ingest.Schema) bool {
	return schema.Type != "" || len(schema.Properties) > 0 || schema.Items != nil
}

// limits size the mock's container. Smaller than a test run's: a mock serves JSON it
// was handed, and a browser-sized allowance would be a container nobody needs.
func (s *Service) limits(ctx context.Context, projectID uuid.UUID) (runner.Limits, error) {
	scope := settings.Target{ProjectID: &projectID}

	cpus, err := s.settings.Float(ctx, "runner.cpus", scope)
	if err != nil {
		return runner.Limits{}, apierr.Internal(fmt.Errorf("read the CPU limit: %w", err))
	}

	limits := runner.Limits{
		CPUs:      minFloat(cpus, 1),
		MemoryMiB: mockMemoryMiB,
		TmpfsMiB:  mockTmpfsMiB,
		PIDs:      mockPIDs,
	}
	return limits, nil
}

const (
	mockMemoryMiB = 256
	mockTmpfsMiB  = 64
	mockPIDs      = 128
)

func (s *Service) runtime(ctx context.Context) (runner.Runtime, error) {
	configured, err := s.settings.String(ctx, "runner.runtime", settings.Target{})
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the container runtime: %w", err))
	}
	return runner.RuntimeFor(configured), nil
}

// reachableHost is the host a client app and a generated suite both use to reach the
// mock.
//
// Configured rather than guessed: the platform cannot know whether its callers are on
// the same machine, on the same bridge, or somewhere else entirely, and a URL that only
// works from one of those is a URL somebody will paste into a broken place. The default
// is the Docker bridge address, which is what a container-run suite can reach.
func (s *Service) reachableHost(ctx context.Context) (string, error) {
	configured, err := s.settings.String(ctx, "mock.host", settings.Target{})
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the mock host: %w", err))
	}

	host := strings.TrimSpace(configured)
	if host == "" {
		return "", apierr.MockUnavailable(
			"No reachable host is configured for mock servers. Set mock.host to the address " +
				"a client application and a runner container can both reach.")
	}

	// Parsed so a value pasted as a URL still works: somebody setting "mock.host" to
	// "http://192.168.1.10/" meant the address.
	if strings.Contains(host, "://") {
		parsed, err := url.Parse(host)
		if err != nil || parsed.Hostname() == "" {
			return "", apierr.Validation(
				"mock.host is not a host this platform can use.",
				map[string]any{"key": "mock.host"})
		}
		host = parsed.Hostname()
	}
	if trimmed, _, err := net.SplitHostPort(host); err == nil {
		host = trimmed
	}
	return host, nil
}

// store writes the running mock's row.
func (s *Service) store(
	ctx context.Context,
	input StartInput,
	routes []Route,
	service *runner.Service,
	mockURL, image string,
) (Mock, error) {
	encodedRoutes, err := json.Marshal(routes)
	if err != nil {
		return Mock{}, apierr.Internal(fmt.Errorf("encode the mock routes: %w", err))
	}
	encodedFaults, err := json.Marshal(input.Faults)
	if err != nil {
		return Mock{}, apierr.Internal(fmt.Errorf("encode the mock faults: %w", err))
	}

	started := service.StartedAt
	actor := input.Actor.UserID

	row, err := s.db.Queries().UpsertMockServer(ctx, dbgen.UpsertMockServerParams{
		ProjectID:   input.ProjectID,
		ContainerID: service.ContainerID,
		Status:      string(StatusRunning),
		Url:         mockURL,
		HostPort:    int32(service.HostPort), //nolint:gosec // A port is 16 bits.
		Routes:      encodedRoutes,
		Faults:      encodedFaults,
		RouteCount:  int32(len(routes)), //nolint:gosec // Bounded by the specification.
		Image:       image,
		Error:       "",
		StartedBy:   &actor,
		StartedAt:   &started,
		StoppedAt:   nil,
	})
	if err != nil {
		return Mock{}, apierr.Internal(fmt.Errorf("record the mock server: %w", err))
	}
	return toMock(row), nil
}

// recordFailure keeps a failed start visible.
func (s *Service) recordFailure(ctx context.Context, input StartInput, routes []Route, cause error) {
	encodedRoutes, err := json.Marshal(routes)
	if err != nil {
		encodedRoutes = []byte("[]")
	}
	encodedFaults, err := json.Marshal(input.Faults)
	if err != nil {
		encodedFaults = []byte("{}")
	}

	actor := input.Actor.UserID
	if _, err := s.db.Queries().UpsertMockServer(ctx, dbgen.UpsertMockServerParams{
		ProjectID:   input.ProjectID,
		ContainerID: "",
		Status:      string(StatusFailed),
		Url:         "",
		HostPort:    0,
		Routes:      encodedRoutes,
		Faults:      encodedFaults,
		RouteCount:  int32(len(routes)), //nolint:gosec // Bounded by the specification.
		Image:       "",
		Error:       cause.Error(),
		StartedBy:   &actor,
		StartedAt:   nil,
		StoppedAt:   nil,
	}); err != nil {
		slog.WarnContext(ctx, "record a failed mock start",
			"project_id", input.ProjectID, "error", err)
	}
}

func toMock(row dbgen.MockServer) Mock {
	mock := Mock{
		ProjectID:   row.ProjectID,
		ContainerID: row.ContainerID,
		Status:      Status(row.Status),
		URL:         row.Url,
		HostPort:    int(row.HostPort),
		RouteCount:  int(row.RouteCount),
		Image:       row.Image,
		Error:       row.Error,
		StartedBy:   row.StartedBy,
		StartedAt:   row.StartedAt,
		StoppedAt:   row.StoppedAt,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	// A row whose routes do not decode is still a row worth returning: the status and
	// the URL are what a caller came for, and the routes are detail.
	if len(row.Routes) > 0 {
		_ = json.Unmarshal(row.Routes, &mock.Routes) //nolint:errcheck // Detail, not the answer.
	}
	if len(row.Faults) > 0 {
		_ = json.Unmarshal(row.Faults, &mock.Faults) //nolint:errcheck // Detail, not the answer.
	}
	return mock
}

func minFloat(value, ceiling float64) float64 {
	if value <= 0 || value > ceiling {
		return ceiling
	}
	return value
}
