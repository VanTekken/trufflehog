package log

import (
	"errors"
	"io"
	"time"

	"github.com/TheZeroSlave/zapsentry"
	"github.com/getsentry/sentry-go"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// global log level control (see SetLevel)
	logLevel zap.AtomicLevel = zap.NewAtomicLevel()
)

type logConfig struct {
	core    zapcore.Core
	cleanup func() error
	err     error
}

// New creates a new log object with the provided configurations. If no sinks
// are provided, a no-op sink will be used. Returns the logger and a cleanup
// function that should be executed before the program exits.
func New(service string, configs ...logConfig) (logr.Logger, func() error) {
	var cores []zapcore.Core
	var cleanupFuncs []func() error

	// create cores for the logger
	for _, config := range configs {
		if config.err != nil {
			continue
		}
		cores = append(cores, config.core)
		if config.cleanup != nil {
			cleanupFuncs = append(cleanupFuncs, config.cleanup)
		}
	}
	// create logger
	zapLogger := zap.New(zapcore.NewTee(cores...))
	cleanupFuncs = append(cleanupFuncs, zapLogger.Sync)
	logger := zapr.NewLogger(zapLogger).WithName(service)

	// report the errors we encountered in the configs
	for _, config := range configs {
		if config.err != nil {
			logger.Error(config.err, "error configuring logger")
		}
	}

	return logger, firstErrorFunc(cleanupFuncs...)
}

// SetLevel sets the log level for all loggers created with WithJSONSink and
// WithConsoleSink.
func SetLevel(level int8) {
	// Zap's levels get more verbose as the number gets smaller, as explained
	// by zapr here: https://github.com/go-logr/zapr#increasing-verbosity
	// For example setting the level to -2 below, means log.V(2) will be enabled.
	logLevel.SetLevel(zapcore.Level(-level))
}

// WithSentry adds sentry integration to the logger. This configuration may
// fail, in which case, sentry will not be added and execution will continue
// normally.
func WithSentry(opts sentry.ClientOptions, tags map[string]string) logConfig {
	client, err := sentry.NewClient(opts)
	if err != nil {
		return logConfig{err: err}
	}

	// create sentry core
	cfg := zapsentry.Configuration{
		Tags:              tags,
		Level:             zapcore.ErrorLevel,
		EnableBreadcrumbs: true,
		BreadcrumbLevel:   zapcore.InfoLevel,
	}
	core, err := zapsentry.NewCore(cfg, zapsentry.NewSentryClientFromClient(client))
	if err != nil {
		return logConfig{err: err}
	}

	return logConfig{
		core: core,
		cleanup: func() error {
			sentry.Flush(5 * time.Second)
			return nil
		},
	}
}

// WithJSONSink adds a JSON encoded output to the logger.
func WithJSONSink(sink io.Writer) logConfig {
	return logConfig{
		core: zapcore.NewCore(
			zapcore.NewJSONEncoder(defaultEncoderConfig()),
			zapcore.Lock(
				zapcore.AddSync(sink),
			),
			logLevel,
		),
	}
}

// WithConsoleSink adds a console-style output to the logger.
func WithConsoleSink(sink io.Writer) logConfig {
	return logConfig{
		core: zapcore.NewCore(
			zapcore.NewConsoleEncoder(defaultEncoderConfig()),
			zapcore.Lock(
				zapcore.AddSync(sink),
			),
			logLevel,
		),
	}
}

func defaultEncoderConfig() zapcore.EncoderConfig {
	conf := zap.NewProductionEncoderConfig()
	// Use more human-readable time format.
	conf.EncodeTime = zapcore.TimeEncoderOfLayout(time.RFC3339)
	return conf
}

// WithCore adds any user supplied zap core to the logger.
func WithCore(core zapcore.Core) logConfig {
	return logConfig{core: core}
}

// AddSentry initializes a sentry client and extends an existing
// logr.Logger with the hook.
func AddSentry(l logr.Logger, opts sentry.ClientOptions, tags map[string]string) (logr.Logger, func() error, error) {
	conf := WithSentry(opts, tags)
	if conf.err != nil {
		return l, nil, conf.err
	}
	return AddSink(l, conf)
}

// AddSink extends an existing logr.Logger with a new sink. It returns the new
// logr.Logger, a cleanup function, and an error.
func AddSink(l logr.Logger, sink logConfig) (logr.Logger, func() error, error) {
	zapLogger, err := getZapLogger(l)
	if err != nil {
		return l, nil, errors.New("unsupported logr implementation")
	}
	zapLogger = zapLogger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, sink.core)
	}))
	return zapr.NewLogger(zapLogger), firstErrorFunc(zapLogger.Sync, sink.cleanup), nil
}

// getZapLogger is a helper function that gets the underlying zap logger from a
// logr.Logger interface.
func getZapLogger(l logr.Logger) (*zap.Logger, error) {
	if u, ok := l.GetSink().(zapr.Underlier); ok {
		return u.GetUnderlying(), nil
	}
	return nil, errors.New("not a zapr logger")
}

// firstErrorFunc is a helper function that returns a function that executes
// all provided args and returns the first error, if any.
func firstErrorFunc(fs ...func() error) func() error {
	return func() error {
		var firstErr error = nil
		for _, f := range fs {
			if f == nil {
				continue
			}
			if err := f(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
}
