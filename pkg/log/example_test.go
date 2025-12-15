package log_test

import (
	"testing"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/pkg/log"
)

func TestLogger(t *testing.T) {
	opts := &log.Options{
		Level:             "debug",
		Format:            "json",
		DisableCaller:     false,
		DisableStacktrace: false,
		OutputPaths:       []string{"stdout"},
	}

	log.Init(opts)

	log.Debugw("This is a debug message", "key1", "value1", "key2", 123)
	log.Infow("This is an info message", "key", "value")
	log.Warnw("This is a warning message", "timestamp", time.Now())
	log.Errorw("This is an error message", "error", "something went wrong")

	// Note: Panicw and Fatalw will terminate program execution, so they should be used with caution in tests.
	// log.Panicw("This is a panic message", "reason", "unexpected situation")
	// log.Fatalw("This is a fatal message", "reason", "critical failure")

	// Ensures that the log buffer is flushed.
	log.Sync()
}
