package cmd

import (
	"github.com/spf13/cobra"
)

func runFreeMain(cmd *cobra.Command, args []string) error {
	_, err := sharedRunner.FreeMain(0)
	return err
}
