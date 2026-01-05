package cmd

import (
	"fmt"

	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var quotaCmd = &cobra.Command{
	Use:   "quota",
	Short: "Calculate and print total used and available quota for each provider",
	Long: `Calculates and prints the total used quota and total available quota for each provider,
aggregating all accounts. It also performs a cross-check to ensure that the used quota
for any provider does not exceed the available quota of any other provider.`,
	RunE: runQuota,
}

func init() {
	rootCmd.AddCommand(quotaCmd)
}

func runQuota(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	// We don't strictly need pre-flight checks for quota, but we need clients initialized.
	// Pre-flight checks might be good to ensure connectivity.
	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	quotas, err := runner.GetProviderQuotas()
	if err != nil {
		return err
	}

	// Print quotas
	fmt.Println("Quota Summary:")
	fmt.Println("--------------")
	for _, q := range quotas {
		totalStr := formatBytes(q.Total)
		usedStr := formatBytes(q.Used)
		freeStr := formatBytes(q.Free)

		if q.Total == -1 {
			totalStr = "Unlimited"
			freeStr = "Unlimited"
		}

		fmt.Printf("[%s]\n", q.Provider)
		fmt.Printf("  Total: %s\n", totalStr)
		fmt.Printf("  Used:  %s\n", usedStr)
		fmt.Printf("  Free:  %s\n", freeStr)
		fmt.Println()
	}

	// Cross-check
	// Error if used quota for any provider is bigger than total quota for any provider
	var errs []string

	for _, q1 := range quotas {
		for _, q2 := range quotas {
			// Skip if q2 is unlimited
			if q2.Total == -1 {
				continue
			}

			if q1.Used > q2.Total {
				errs = append(errs, fmt.Sprintf("Provider %s Used (%s) > Provider %s Total (%s)",
					q1.Provider, formatBytes(q1.Used), q2.Provider, formatBytes(q2.Total)))
			}
		}
	}

	if len(errs) > 0 {
		logger.Error("Quota cross-check failed:")
		for _, e := range errs {
			logger.Error("- %s", e)
		}
		return fmt.Errorf("quota cross-check failed")
	}

	logger.Info("Quota check passed")
	return nil
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
