package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
)

func baseOptions() Options {
	return Options{
		ServiceName:    "qavia-api",
		ServiceVersion: "test",
		Environment:    "development",
		SampleRatio:    1,
	}
}

// The no-op path must still return a usable tracer and a shutdown function, so
// instrumentation is written unconditionally and main never branches on which
// exporter was configured.
func TestSetupWithoutAnExporterStillWorks(t *testing.T) {
	opts := baseOptions()
	opts.Exporter = ExporterNone

	shutdown, err := Setup(context.Background(), opts)
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))

	ctx, span := StartSpan(context.Background(), "noop")
	span.End()
	require.NotNil(t, ctx)
	require.False(t, span.SpanContext().IsSampled())
}

// An empty Exporter is the same as none: a missing setting must not stop boot.
func TestSetupTreatsEmptyExporterAsNone(t *testing.T) {
	shutdown, err := Setup(context.Background(), baseOptions())
	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))
}

func TestSetupStdoutProducesRealSpans(t *testing.T) {
	opts := baseOptions()
	opts.Exporter = ExporterStdout

	shutdown, err := Setup(context.Background(), opts)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, shutdown(context.Background())) })

	_, span := StartSpan(context.Background(), "job.execute")
	require.True(t, span.SpanContext().IsValid())
	require.True(t, span.SpanContext().IsSampled())
	span.End()
}

func TestSetupRejectsOTLPWithoutAnEndpoint(t *testing.T) {
	opts := baseOptions()
	opts.Exporter = ExporterOTLP

	_, err := Setup(context.Background(), opts)
	require.ErrorContains(t, err, "no endpoint configured")
}

func TestSetupRejectsAnUnknownExporter(t *testing.T) {
	opts := baseOptions()
	opts.Exporter = Exporter("jaeger")

	_, err := Setup(context.Background(), opts)
	require.ErrorContains(t, err, "unknown trace exporter")
}

// W3C propagation is what lets the Python service and a CI webhook join the same
// trace, so it is installed on every path including no-op.
func TestPropagatorsAreAlwaysInstalled(t *testing.T) {
	opts := baseOptions()
	opts.Exporter = ExporterNone

	_, err := Setup(context.Background(), opts)
	require.NoError(t, err)

	fields := otel.GetTextMapPropagator().Fields()
	require.Contains(t, fields, "traceparent")
	require.Contains(t, fields, "baggage")
}
