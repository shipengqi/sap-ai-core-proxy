package log

import (
	"context"
	"sync"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/pkg/contextx"
	"github.com/shipengqi/sap-ai-core-proxy/pkg/known"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger defines the logging interface.
// This interface includes logging methods that provide support for different log levels.
type Logger interface {
	// Debugw is used to log debug-level messages, typically during development,
	// containing detailed debugging information.
	Debugw(msg string, kvs ...any)

	// Infow is used to log info-level messages, indicating the normal operational state of the system.
	Infow(msg string, kvs ...any)

	// Warnw is used to log warning-level messages, indicating potential issues
	// that do not affect normal system operation.
	Warnw(msg string, kvs ...any)

	// Errorw is used to log error-level messages, indicating errors that occur during system operation
	// and require developer intervention.
	Errorw(msg string, kvs ...any)

	// Panicw is used to log critical-level messages indicating that the system cannot continue running;
	// it logs the message and then triggers a panic.
	Panicw(msg string, kvs ...any)

	// Fatalw is used to log fatal-level messages, indicating that the system cannot continue running;
	// it logs the message and then immediately terminates the program.
	Fatalw(msg string, kvs ...any)

	// Sync is used to flush the log buffer, ensuring that all logs are completely written to the target storage.
	Sync()
}

// zapLogger is a concrete implementation of the Logger interface. It a wrapper of zap.Logger.
type zapLogger struct {
	z *zap.Logger
}

var _ Logger = (*zapLogger)(nil)

var (
	mu sync.Mutex

	// std defines the default global Logger.
	std = New(NewOptions())
)

// Init initializes the global logger instance.
func Init(opts *Options) {
	// Since it assigns a value to the global variable "std",
	// a lock is used here to prevent concurrency issues.
	mu.Lock()
	defer mu.Unlock()

	std = New(opts)
}

// New creates a custom zapLogger object with the provided Options.
// If the Options is nil, the default Options configuration will be used.
func New(opts *Options) *zapLogger {
	if opts == nil {
		opts = NewOptions()
	}

	// Converts the log level string in Options to a zapcore.Level type.
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(opts.Level)); err != nil {
		// If an invalid log level is specified, the info level will be used by default.
		zapLevel = zapcore.InfoLevel
	}

	// Creates an encoder configuration used to control the log output format.
	encoderConfig := zap.NewProductionEncoderConfig()
	// Customizes the MessageKey to "message" for clearer semantics.
	encoderConfig.MessageKey = "message"
	// Customizes the TimeKey to "timestamp" for clearer semantics.
	encoderConfig.TimeKey = "timestamp"
	// Specifies the time serialization function to format timestamps as 2006-01-02 15:04:05.000 for better readability.
	encoderConfig.EncodeTime = func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(t.Format("2006-01-02 15:04:05.000"))
	}
	// Specifies the time.Duration serialization function to serialize time.Duration as
	// a floating-point number representing the elapsed milliseconds.
	encoderConfig.EncodeDuration = func(d time.Duration, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendFloat64(float64(d) / float64(time.Millisecond))
	}

	cfg := &zap.Config{
		DisableCaller:     opts.DisableCaller,
		DisableStacktrace: opts.DisableStacktrace,
		Level:             zap.NewAtomicLevelAt(zapLevel),
		Encoding:          opts.Format,
		EncoderConfig:     encoderConfig,
		OutputPaths:       opts.OutputPaths,
		ErrorOutputPaths:  []string{"stderr"},
	}

	z, err := cfg.Build(zap.AddStacktrace(zapcore.PanicLevel), zap.AddCallerSkip(2))
	if err != nil {
		panic(err)
	}

	// Redirects the standard library’s "log" output to zap.Logger.
	zap.RedirectStdLog(z)

	return &zapLogger{z: z}
}

// Sync calls the underlying zap.Logger’s Sync method to flush buffered logs to disk.
// Should call Sync before main function exiting.
func Sync() {
	std.Sync()
}

func (l *zapLogger) Sync() {
	_ = l.z.Sync()
}

// Debugw outputs logs at the debug level.
func Debugw(msg string, kvs ...any) {
	std.Debugw(msg, kvs...)
}

func (l *zapLogger) Debugw(msg string, kvs ...any) {
	l.z.Sugar().Debugw(msg, kvs...)
}

// Infow outputs logs at the info level.
func Infow(msg string, kvs ...any) {
	std.Infow(msg, kvs...)
}

func (l *zapLogger) Infow(msg string, kvs ...any) {
	l.z.Sugar().Infow(msg, kvs...)
}

// Warnw outputs logs at the warning level.
func Warnw(msg string, kvs ...any) {
	std.Warnw(msg, kvs...)
}

func (l *zapLogger) Warnw(msg string, kvs ...any) {
	l.z.Sugar().Warnw(msg, kvs...)
}

// Errorw outputs logs at the error level.
func Errorw(msg string, kvs ...any) {
	std.Errorw(msg, kvs...)
}

func (l *zapLogger) Errorw(msg string, kvs ...any) {
	l.z.Sugar().Errorw(msg, kvs...)
}

// Panicw outputs logs at the panic level.
func Panicw(msg string, kvs ...any) {
	std.Panicw(msg, kvs...)
}

func (l *zapLogger) Panicw(msg string, kvs ...any) {
	l.z.Sugar().Panicw(msg, kvs...)
}

// Fatalw outputs logs at the fatal level.
func Fatalw(msg string, kvs ...any) {
	std.Fatalw(msg, kvs...)
}

func (l *zapLogger) Fatalw(msg string, kvs ...any) {
	l.z.Sugar().Fatalw(msg, kvs...)
}

// W Parses the provided context, attempts to extract relevant key-value pairs,
// and adds them to the structured logs of zap.Logger.
func W(ctx context.Context) Logger {
	return std.W(ctx)
}

func (l *zapLogger) W(ctx context.Context) Logger {
	lc := l.clone()

	// Defines a mapping that associates context extraction functions
	// with corresponding log field names.
	contextExtractors := map[string]func(context.Context) string{
		known.XRequestID: contextx.RequestID,
		known.XUserID:    contextx.UserID,
	}

	// Iterates through the mapping to extract values from the context and
	// add them to the log.
	for fieldName, extractor := range contextExtractors {
		if val := extractor(ctx); val != "" {
			lc.z = lc.z.With(zap.String(fieldName, val))
		}
	}

	return lc
}

// clone return a deep copy of the zapLogger.
func (l *zapLogger) clone() *zapLogger {
	newLogger := *l
	return &newLogger
}
