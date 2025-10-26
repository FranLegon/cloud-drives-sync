package logger

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// Logger provides structured logging with tags
type Logger struct {
	prefix string
}

// New creates a new logger instance
func New() *Logger {
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(os.Stdout)
	return &Logger{}
}

// WithPrefix returns a new logger with the specified prefix/tag
func (l *Logger) WithPrefix(prefix string) *Logger {
	return &Logger{prefix: prefix}
}

// Info logs an informational message
func (l *Logger) Info(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if l.prefix != "" {
		log.Printf("[%s] %s", l.prefix, msg)
	} else {
		log.Println(msg)
	}
}

// Error logs an error message
func (l *Logger) Error(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if l.prefix != "" {
		log.Printf("[%s] ERROR: %s", l.prefix, msg)
	} else {
		log.Printf("ERROR: %s", msg)
	}
}

// Warning logs a warning message
func (l *Logger) Warning(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if l.prefix != "" {
		log.Printf("[%s] WARNING: %s", l.prefix, msg)
	} else {
		log.Printf("WARNING: %s", msg)
	}
}

// Success logs a success message
func (l *Logger) Success(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if l.prefix != "" {
		log.Printf("[%s] ✓ %s", l.prefix, msg)
	} else {
		log.Printf("✓ %s", msg)
	}
}

// DryRun logs a dry-run action
func (l *Logger) DryRun(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if l.prefix != "" {
		log.Printf("[%s] [DRY RUN] %s", l.prefix, msg)
	} else {
		log.Printf("[DRY RUN] %s", msg)
	}
}

// Fatal logs a fatal error and exits
func (l *Logger) Fatal(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if l.prefix != "" {
		log.Fatalf("[%s] FATAL: %s", l.prefix, msg)
	} else {
		log.Fatalf("FATAL: %s", msg)
	}
}

// NormalizePath normalizes a path to lowercase with forward slashes
func NormalizePath(path string) string {
	normalized := strings.ToLower(path)
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	return normalized
}
