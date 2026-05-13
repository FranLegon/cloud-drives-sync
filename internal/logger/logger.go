package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

type LogLevel int

const (
	LogLevelInfo LogLevel = iota
	LogLevelWarning
	LogLevelError
)

var (
	infoLogger    *log.Logger
	warningLogger *log.Logger
	errorLogger   *log.Logger
	currentLevel  = LogLevelInfo
	mu            sync.RWMutex
)

func init() {
	infoLogger = log.New(os.Stdout, "", 0)
	warningLogger = log.New(os.Stdout, "", 0)
	errorLogger = log.New(os.Stderr, "", 0)
}

// SetLevel sets the minimum log level to display
func SetLevel(level LogLevel) {
	mu.Lock()
	currentLevel = level
	mu.Unlock()
}

// SetOutput sets the output destination for the loggers
func SetOutput(w io.Writer) {
	mu.Lock()
	infoLogger.SetOutput(w)
	warningLogger.SetOutput(w)
	errorLogger.SetOutput(w)
	mu.Unlock()
}

func formatTags(tags []string) string {
	if len(tags) > 0 {
		return fmt.Sprintf("[%s] ", strings.Join(tags, "]["))
	}
	return ""
}

// Info logs an informational message
func Info(format string, v ...interface{}) {
	mu.RLock()
	defer mu.RUnlock()
	if currentLevel <= LogLevelInfo {
		infoLogger.Printf(format, v...)
	}
}

// InfoTagged logs an informational message with tags
func InfoTagged(tags []string, format string, v ...interface{}) {
	mu.RLock()
	defer mu.RUnlock()
	if currentLevel <= LogLevelInfo {
		infoLogger.Printf(formatTags(tags)+format, v...)
	}
}

// Warning logs a warning message
func Warning(format string, v ...interface{}) {
	mu.RLock()
	defer mu.RUnlock()
	if currentLevel <= LogLevelWarning {
		warningLogger.Printf("WARNING: "+format, v...)
	}
}

// WarningTagged logs a warning message with tags
func WarningTagged(tags []string, format string, v ...interface{}) {
	mu.RLock()
	defer mu.RUnlock()
	if currentLevel <= LogLevelWarning {
		warningLogger.Printf("WARNING: "+formatTags(tags)+format, v...)
	}
}

// Error logs an error message
func Error(format string, v ...interface{}) {
	mu.RLock()
	defer mu.RUnlock()
	if currentLevel <= LogLevelError {
		errorLogger.Printf("ERROR: "+format, v...)
	}
}

// ErrorTagged logs an error message with tags
func ErrorTagged(tags []string, format string, v ...interface{}) {
	mu.RLock()
	defer mu.RUnlock()
	if currentLevel <= LogLevelError {
		errorLogger.Printf("ERROR: "+formatTags(tags)+format, v...)
	}
}

// DryRun logs a dry run action
func DryRun(format string, v ...interface{}) {
	mu.RLock()
	defer mu.RUnlock()
	infoLogger.Printf("[DRY RUN] "+format, v...)
}

// DryRunTagged logs a dry run action with tags
func DryRunTagged(tags []string, format string, v ...interface{}) {
	mu.RLock()
	defer mu.RUnlock()
	infoLogger.Printf("[DRY RUN] "+formatTags(tags)+format, v...)
}
