package cmd

import (
	"github.com/spf13/cobra"
)

func runBalanceStorage(cmd *cobra.Command, args []string) error {
	return sharedRunner.BalanceStorage(0)
}
