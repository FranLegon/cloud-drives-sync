package cmd

var SafeMode bool

func init() {
	rootCmd.PersistentFlags().BoolVarP(&SafeMode, "safe", "s", false, "Dry run mode (no write/delete/permission changes)")
}
