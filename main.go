package main

import (
	"cloud-drives-sync/cmd"
)

// main is the entry point for the entire application. Its sole responsibility
// is to execute the root command of the CLI structure defined in the 'cmd' package.
// All logic, argument parsing, and flag handling are managed by Cobra within that package.
func main() {
	cmd.Execute()
}
