package logger

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestInfoLogging(t *testing.T) {
	var buf bytes.Buffer
	infoLogger = log.New(&buf, "", 0)

	Info("Test message: %s", "info")
	output := buf.String()

	if !strings.Contains(output, "Test message: info") {
		t.Errorf("Expected log to contain 'Test message: info', got: %s", output)
	}
}

func TestInfoTagged(t *testing.T) {
	var buf bytes.Buffer
	infoLogger = log.New(&buf, "", 0)

	InfoTagged([]string{"Google", "test@example.com"}, "Test message")
	output := buf.String()

	if !strings.Contains(output, "[Google][test@example.com]") {
		t.Errorf("Expected log to contain tags, got: %s", output)
	}
	if !strings.Contains(output, "Test message") {
		t.Errorf("Expected log to contain message, got: %s", output)
	}
}

func TestDryRun(t *testing.T) {
	var buf bytes.Buffer
	infoLogger = log.New(&buf, "", 0)

	DryRun("Test action")
	output := buf.String()

	if !strings.Contains(output, "[DRY RUN]") {
		t.Errorf("Expected log to contain '[DRY RUN]', got: %s", output)
	}
}

func TestLogLevel(t *testing.T) {
	var buf bytes.Buffer
	infoLogger = log.New(&buf, "", 0)

	// Set level to Error
	SetLevel(LogLevelError)

	// Info should not log
	Info("This should not appear")
	if buf.Len() > 0 {
		t.Error("Info logged when level was set to Error")
	}

	// Reset to Info for other tests
	SetLevel(LogLevelInfo)
}

func TestMain(m *testing.M) {
	// Setup: redirect loggers for testing
	code := m.Run()
	os.Exit(code)
}
