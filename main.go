package main

import (
	"cloud-drives-sync/cmd"
	"os"
)

func main() {
	// The cobra framework handles command execution and error catching.
	// We just need to kick it off.
	if err := cmd.Execute(); err != nil {
		// Cobra prints the error, so we just need to exit.
		os.Exit(1)
	}
}
