package log

import (
	"go.uber.org/zap/zapcore"
)

// Options Defines the struct for log configuration options.
// This struct allows customization of the log output format, level, and other related configurations.
type Options struct {
	// DisableCaller Whether to disable caller information.
	// If set to false (the default value), the log will include the filename
	// and line number of the caller, for example: "caller":"main.go:42".
	DisableCaller bool
	// DisableStacktrace Whether to disable stack information.
	// If set to false (the default value), stack trace information will be
	// printed when the log level is panic or higher.
	DisableStacktrace bool
	// Level Specifies the log level.
	// Optional values: debug, info, warn, error, dpanic, panic, and fatal.
	// The default value is "info".
	Level string
	// Format Specifies the log output format.
	// Optional values: console, json.
	// The default value is "console".
	Format string
	// OutputPaths Specifies the log output destination.
	// The default value is "stdout", but a file path or other output target can also be specified.
	OutputPaths []string
}

// NewOptions Creates and returns an "Options" object with default values.
// This method initializes the log configuration options, providing default log level, format, and output destination.
func NewOptions() *Options {
	return &Options{
		// Enables caller information by default.
		DisableCaller: false,
		// Enables stack trace information by default.
		DisableStacktrace: false,
		// The default log level is "info".
		Level: zapcore.InfoLevel.String(),
		// The default log output format is "console".
		Format: "console",
		// The default log output destination is "stdout"
		OutputPaths: []string{"stdout"},
	}
}
