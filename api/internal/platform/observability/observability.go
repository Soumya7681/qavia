// Package observability wires OpenTelemetry.
//
// A single user action spans the web app, the Go API, the queue, the Python
// service, a provider API, and a Docker container. Without distributed tracing,
// "why did this take 11 minutes" is unanswerable, and retrofitting tracing is
// far more work than adding it in phase 0 (tech-stack.md 12).
//
// The exporter is chosen from settings, not from an environment variable, so
// Setup is called after the settings service is available rather than at the very
// top of main.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Exporter selects where spans go.
type Exporter string

const (
	// ExporterNone installs a no-op tracer. Everything still compiles and every
	// span call is free, so instrumentation can be written unconditionally.
	ExporterNone Exporter = "none"

	// ExporterStdout prints spans. The development default: no collector to run.
	ExporterStdout Exporter = "stdout"

	// ExporterOTLP ships spans to a collector over gRPC.
	ExporterOTLP Exporter = "otlp"
)

// Options comes from the settings registry, not the environment.
type Options struct {
	ServiceName    string
	ServiceVersion string
	Environment    string

	Exporter     Exporter
	OTLPEndpoint string

	// SampleRatio applies to root spans. 1.0 in development, lower under load.
	SampleRatio float64
}

// ShutdownFunc flushes and stops the pipeline. main defers it, because a span
// buffered at exit is a span nobody ever sees.
type ShutdownFunc func(context.Context) error

// Setup installs the global tracer provider and propagators.
//
// It returns a shutdown function even on the no-op path, so the caller never has
// to branch on which exporter was configured.
func Setup(ctx context.Context, opts Options) (ShutdownFunc, error) {
	// W3C trace context plus baggage, always. This is what lets the Python
	// service and a webhook from CI join the same trace.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if opts.Exporter == ExporterNone || opts.Exporter == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := newExporter(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("build trace exporter: %w", err)
	}

	// NewSchemaless rather than NewWithAttributes: resource.Default() carries the
	// SDK's own semconv schema URL, and pinning a different one here makes Merge
	// fail with a schema conflict on every SDK upgrade.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(opts.ServiceName),
		semconv.ServiceVersion(opts.ServiceVersion),
		attribute.String("deployment.environment", opts.Environment),
	))
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}

	ratio := opts.SampleRatio
	if ratio <= 0 {
		ratio = 1
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		// ParentBased keeps a sampled trace sampled all the way through, so a
		// trace never has a hole in the middle.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)
	otel.SetTracerProvider(provider)

	// An exporter that cannot reach its collector must never take the process
	// with it. Tracing is diagnostics, not a dependency.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.WarnContext(ctx, "opentelemetry error", "error", err)
	}))

	return func(shutdownCtx context.Context) error {
		return errors.Join(provider.ForceFlush(shutdownCtx), provider.Shutdown(shutdownCtx))
	}, nil
}

func newExporter(ctx context.Context, opts Options) (sdktrace.SpanExporter, error) {
	switch opts.Exporter {
	case ExporterStdout:
		return stdouttrace.New(stdouttrace.WithoutTimestamps())
	case ExporterOTLP:
		if opts.OTLPEndpoint == "" {
			return nil, errors.New(
				"otlp exporter selected but no endpoint configured; set it in Settings, Observability")
		}
		return otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(opts.OTLPEndpoint),
			otlptracegrpc.WithInsecure(),
		)
	default:
		return nil, fmt.Errorf("unknown trace exporter %q", opts.Exporter)
	}
}

// tracerName scopes every span this module starts.
const tracerName = "github.com/hyscaler/qavia/api"

// Tracer returns the shared tracer. Job execution, provider calls, and container
// runs all start their spans through it.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// StartSpan is a small convenience so job and runner code does not repeat the
// tracer lookup.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}
