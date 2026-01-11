package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
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
)

func init() {
	infoLogger = log.New(os.Stdout, "", 0)
	warningLogger = log.New(os.Stdout, "", 0)
	errorLogger = log.New(os.Stderr, "", 0)
}

// SetLevel sets the minimum log level to display
func SetLevel(level LogLevel) {
	currentLevel = level
}

// SetOutput sets the output destination for the loggers
func SetOutput(w io.Writer) { // Changed signature to accept io.Writer
	infoLogger.SetOutput(w)
	warningLogger.SetOutput(w)
	errorLogger.SetOutput(w)
}

// Info logs an informational message
func Info(format string, v ...interface{}) {
	if currentLevel <= LogLevelInfo {
		infoLogger.Printf(format, v...)
	}
}

// InfoTagged logs an informational message with tags
func InfoTagged(tags []string, format string, v ...interface{}) {
	if currentLevel <= LogLevelInfo {
		prefix := ""
		if len(tags) > 0 {
			prefix = fmt.Sprintf("[%s] ", strings.Join(tags, "]["))
		}
		infoLogger.Printf(prefix+format, v...)
	}
}

// Warning logs a warning message
func Warning(format string, v ...interface{}) {
	if currentLevel <= LogLevelWarning {
		warningLogger.Printf("WARNING: "+format, v...)
	}
}

// WarningTagged logs a warning message with tags
func WarningTagged(tags []string, format string, v ...interface{}) {
	if currentLevel <= LogLevelWarning {
		prefix := "WARNING: "
		if len(tags) > 0 {
			prefix = fmt.Sprintf("WARNING: [%s] ", strings.Join(tags, "]["))
		}
		warningLogger.Printf(prefix+format, v...)
	}
}

// Error logs an error message
func Error(format string, v ...interface{}) {
	if currentLevel <= LogLevelError {
		errorLogger.Printf("ERROR: "+format, v...)
	}
}

// ErrorTagged logs an error message with tags
func ErrorTagged(tags []string, format string, v ...interface{}) {
	if currentLevel <= LogLevelError {
		prefix := "ERROR: "
		if len(tags) > 0 {
			prefix = fmt.Sprintf("ERROR: [%s] ", strings.Join(tags, "]["))
		}
		errorLogger.Printf(prefix+format, v...)
	}
}

// DryRun logs a dry run action
func DryRun(format string, v ...interface{}) {
	infoLogger.Printf("[DRY RUN] "+format, v...)
}

// DryRunTagged logs a dry run action with tags
func DryRunTagged(tags []string, format string, v ...interface{}) {
	prefix := "[DRY RUN] "
	if len(tags) > 0 {
		prefix = fmt.Sprintf("[DRY RUN] [%s] ", strings.Join(tags, "]["))
	}
	infoLogger.Printf(prefix+format, v...)
}
