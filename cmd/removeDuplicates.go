package cmd

import (
	"fmt"

	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/spf13/cobra"
)

func runRemoveDuplicates(cmd *cobra.Command, args []string) error {
	return RemoveDuplicatesAction(sharedRunner, true)
}

func RemoveDuplicatesAction(runner *task.Runner, updateMetadata bool) error {
	return fmt.Errorf("remove-duplicates is no longer supported: calculated_id has been removed")
}

func runRemoveDuplicatesUnsafe(cmd *cobra.Command, args []string) error {
	return RemoveDuplicatesUnsafeAction(sharedRunner, true)
}

func RemoveDuplicatesUnsafeAction(runner *task.Runner, updateMetadata bool) error {
	return fmt.Errorf("remove-duplicates-unsafe is no longer supported: calculated_id has been removed")
}
