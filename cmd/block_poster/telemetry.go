package main

import (
	"context"
	"os"
	"runtime"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func setupOTel(ctx context.Context) (func(context.Context) error, error) {
	if os.Getenv("OTEL_METRICS_EXPORTER") == "" {
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("shinzo-generator"),
			semconv.ServiceVersion(indexer.Version),
			semconv.HostArchKey.String(runtime.GOARCH),
		),
		resource.WithOSType(),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES can override
	)
	if err != nil {
		return nil, err
	}

	metricReader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader), sdkmetric.WithResource(res))
	otel.SetMeterProvider(meterProvider)

	return meterProvider.Shutdown, nil
}
