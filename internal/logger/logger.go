package logger

import (
	"fmt"
	"log"
	"os"
)

var (
	// infoLogger handles standard, non-error output to the console (stdout).
	infoLogger = log.New(os.Stdout, "", 0)
	// errorLogger handles all error-level output to the console (stderr).
	errorLogger = log.New(os.Stderr, "ERROR: ", 0)
)

// Info logs a standard, formatted message to stdout.
func Info(format string, v ...interface{}) {
	infoLogger.Printf(format, v...)
}

// TaggedInfo logs a standard, formatted message to stdout, prefixed with a tag
// like '[Google]' or '[main.user@gmail.com]'.
func TaggedInfo(tag, format string, v ...interface{}) {
	infoLogger.Printf(fmt.Sprintf("[%s] %s", tag, format), v...)
}

// Error logs an error, formatted message to stderr.
func Error(format string, v ...interface{}) {
	errorLogger.Printf(format, v...)
}

// TaggedError logs a tagged, formatted error message to stderr.
func TaggedError(tag, format string, v ...interface{}) {
	errorLogger.Printf(fmt.Sprintf("[%s] %s", tag, format), v...)
}

// Fatal logs a final error message and exits the application with a non-zero
// status code, making it suitable for scripting.
func Fatal(format string, v ...interface{}) {
	Error(format, v...)
	os.Exit(1)
}
