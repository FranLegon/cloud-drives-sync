//go:build !auto

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	syncShareWithMain  bool
	syncGetMetadata    bool
	syncQuota          bool
	syncFreeMain       bool
	syncBalanceStorage bool
	syncSyncProviders  bool
	syncUnsyncedFiles  bool
)

// registerSyncActionFlags registers the mutually-exclusive sync action flags and the
// dry-run flag. Only present in the default (non-auto) build.
func registerSyncActionFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&safeMode, "safe", "s", false, "Dry run mode - print what would change without touching the cloud")

	cmd.Flags().BoolVar(&syncShareWithMain, "share-with-main", false, "Verify and repair backup access to the shared structure")
	cmd.Flags().BoolVar(&syncGetMetadata, "get-metadata", false, "Scan every account and update the local database")
	cmd.Flags().BoolVar(&syncQuota, "quota", false, "Report used/available space per provider")
	cmd.Flags().BoolVar(&syncFreeMain, "free-main", false, "Move all file content off the main account to backups")
	cmd.Flags().BoolVar(&syncBalanceStorage, "balance-storage", false, "Rebalance nearly-full backup accounts within a provider")
	cmd.Flags().BoolVar(&syncSyncProviders, "sync-providers", false, "Apply all synchronization rules across providers")
	cmd.Flags().BoolVar(&syncUnsyncedFiles, "sync-unsynced-files", false, "Move Google backup root files into cloud-drives-sync-aux/unsynced-from-backups")
}

// dispatchSyncAction runs the single selected sync action flag. It returns handled=true
// when an action flag was set (mutually exclusive); handled=false means no action flag
// was given and the caller should run the full pipeline.
func dispatchSyncAction(cmd *cobra.Command) (bool, error) {
	type action struct {
		set bool
		run func(*cobra.Command, []string) error
	}
	actions := []action{
		{syncShareWithMain, runShareWithMain},
		{syncGetMetadata, runGetMetadata},
		{syncQuota, runQuota},
		{syncFreeMain, runFreeMain},
		{syncBalanceStorage, runBalanceStorage},
		{syncSyncProviders, runSyncProviders},
		{syncUnsyncedFiles, runSyncUnsyncedFiles},
	}

	var selected func(*cobra.Command, []string) error
	count := 0
	for _, a := range actions {
		if a.set {
			count++
			selected = a.run
		}
	}
	if count == 0 {
		return false, nil
	}
	if count > 1 {
		return true, fmt.Errorf("sync action flags are mutually exclusive; provide at most one")
	}
	return true, selected(cmd, nil)
}
