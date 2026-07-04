//go:build auto

package cmd

import "github.com/spf13/cobra"

// registerSyncActionFlags is a no-op in auto builds: only `sync -p` (the full pipeline)
// is exposed for headless scheduled runs.
func registerSyncActionFlags(cmd *cobra.Command) {}

// dispatchSyncAction never handles an action in auto builds; sync always runs the full pipeline.
func dispatchSyncAction(cmd *cobra.Command) (bool, error) {
	return false, nil
}
