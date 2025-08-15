package logger

import (
	"fmt"
	"log"
	"os"
)

var (
	infoLog   = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime)
	warnLog   = log.New(os.Stderr, "WARN: ", log.Ldate|log.Ltime)
	errorLog  = log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime)
	dryRunLog = log.New(os.Stdout, "[DRY RUN] ", log.Ldate|log.Ltime)
)

// Info logs a standard informational message.
func Info(format string, v ...interface{}) {
	infoLog.Printf(format, v...)
}

// TaggedInfo logs an informational message with a prefix tag (e.g., provider or email).
func TaggedInfo(tag, format string, v ...interface{}) {
	infoLog.Printf(fmt.Sprintf("[%s] %s", tag, format), v...)
}

// Warn logs a non-fatal error message that does not terminate the program.
func Warn(tag string, err error, format string, v ...interface{}) {
	if err != nil {
		warnLog.Printf(fmt.Sprintf("[%s] %s: %v", tag, format, err), v...)
	} else {
		warnLog.Printf(fmt.Sprintf("[%s] %s", tag, format), v...)
	}
}

// Error logs a fatal error message and terminates the program with a non-zero exit code.
func Error(err error, format string, v ...interface{}) {
	if err != nil {
		errorLog.Printf(format+": %v", append(v, err)...)
	} else {
		errorLog.Printf(format, v...)
	}
	os.Exit(1)
}

// DryRun logs an action that would have been taken if the --safe flag were not present.
func DryRun(tag, format string, v ...interface{}) {
	dryRunLog.Printf(fmt.Sprintf("[%s] %s", tag, format), v...)
}
