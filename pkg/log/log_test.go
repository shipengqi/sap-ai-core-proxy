package log

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var mockLogger *zapLogger

func TestMain(m *testing.M) {
	opts := &Options{
		Level:             "debug",
		DisableCaller:     false,
		DisableStacktrace: false,
		Format:            "json",
		OutputPaths:       []string{"stdout"},
	}
	Init(opts)
	mockLogger = std
	m.Run()
}

func TestLoggerMethods(t *testing.T) {
	assert.NotPanics(t, func() {
		Debugw("debug message", "key1", "value1")
		Infow("info message", "key2", "value2")
		Warnw("warn message", "key3", "value3")
		Errorw("error message", "key4", "value4")
	}, "Log methods should not cause a panic in this test")

	assert.Panics(t, func() {
		Panicw("fatal message", "key6", "value6")
	}, "Panicw should cause a panic and exit the program")
}

func TestLoggerInitialization(t *testing.T) {
	opts := NewOptions()
	logger := New(opts)

	assert.NotNil(t, logger, "Logger should not be nil after initialization")
	assert.IsType(t, &zapLogger{}, logger, "Logger should be of type *zapLogger")
}

func TestSync(t *testing.T) {
	assert.NotPanics(t, func() {
		Sync() // 确保 Sync 不会引发恐慌
	}, "Sync should not panic")
}
