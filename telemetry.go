package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// multiHandler mantém o logging estruturado em stdout e envia os mesmos eventos por OTLP.
type multiHandler struct{ handlers []slog.Handler }

func (h multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}
func (h multiHandler) Handle(ctx context.Context, rec slog.Record) error {
	var errs []error
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, rec.Level) {
			if err := handler.Handle(ctx, rec.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
func (h multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := multiHandler{handlers: make([]slog.Handler, 0, len(h.handlers))}
	for _, handler := range h.handlers {
		next.handlers = append(next.handlers, handler.WithAttrs(attrs))
	}
	return next
}
func (h multiHandler) WithGroup(name string) slog.Handler {
	next := multiHandler{handlers: make([]slog.Handler, 0, len(h.handlers))}
	for _, handler := range h.handlers {
		next.handlers = append(next.handlers, handler.WithGroup(name))
	}
	return next
}

func setupTelemetry(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	disabled := os.Getenv("OTEL_SDK_DISABLED") == "true"
	if disabled || os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	resourceAttrs := []attribute.KeyValue{
		attribute.String("service.name", serviceName),
		attribute.String("service.namespace", "toggle-master"),
		attribute.String("deployment.environment.name", "prod"),
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(resourceAttrs...),
	)
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	ratio := 1.0
	if raw := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 && parsed <= 1 {
			ratio = parsed
		}
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithBatcher(traceExporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
			sdkmetric.WithInterval(30*time.Second))),
	)
	otel.SetMeterProvider(mp)

	logExporter, err := otlploghttp.New(ctx)
	if err != nil {
		_ = mp.Shutdown(ctx)
		_ = tp.Shutdown(ctx)
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)
	otel.SetLoggerProvider(lp)
	slog.SetDefault(slog.New(multiHandler{handlers: []slog.Handler{
		slog.NewJSONHandler(os.Stdout, nil),
		otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(lp)),
	}}))

	return func(stopCtx context.Context) error {
		return errors.Join(lp.Shutdown(stopCtx), mp.Shutdown(stopCtx), tp.Shutdown(stopCtx))
	}, nil
}
