//go:build auto

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

const taskName = "cloud-drives-sync"

var (
	setFlag     bool
	disableFlag bool
)

var autoCmd = &cobra.Command{
	Use:   "auto",
	Short: "Manage automatic scheduled synchronization",
	Long: `Create or remove a scheduled task (Windows) or systemd timer (Linux)
that runs the sync command every 8 hours.

Use --set with --password to create the schedule.
Use --disable to remove it.`,
	RunE: runAuto,
}

func init() {
	autoCmd.Flags().BoolVar(&setFlag, "set", false, "Create the scheduled task/service")
	autoCmd.Flags().BoolVarP(&disableFlag, "disable", "d", false, "Remove the scheduled task/service")
	rootCmd.AddCommand(autoCmd)
}

func runAuto(cmd *cobra.Command, args []string) error {
	if !setFlag && !disableFlag {
		return showStatus()
	}
	if setFlag && disableFlag {
		return fmt.Errorf("cannot use --set and --disable together")
	}

	if setFlag {
		if passwordFlag == "" {
			return fmt.Errorf("--password (-p) is required when using --set")
		}
		return setupSchedule()
	}

	return removeSchedule()
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func showStatus() error {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("schtasks", "/query", "/tn", taskName).CombinedOutput()
		if err != nil {
			logger.Info("Scheduled task %q is NOT installed.", taskName)
			return nil
		}
		logger.Info("Scheduled task %q is installed:\n%s", taskName, strings.TrimSpace(string(out)))
	case "linux":
		out, err := exec.Command("systemctl", "--user", "is-active", taskName+".timer").CombinedOutput()
		status := strings.TrimSpace(string(out))
		if err != nil || status != "active" {
			logger.Info("Systemd timer %q is NOT active (status: %s).", taskName, status)
			return nil
		}
		logger.Info("Systemd timer %q is active.", taskName)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Setup
// ---------------------------------------------------------------------------

func setupSchedule() error {
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine binary path: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return fmt.Errorf("failed to resolve binary path: %w", err)
	}

	switch runtime.GOOS {
	case "windows":
		return setupWindows(binaryPath)
	case "linux":
		return setupLinux(binaryPath)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func setupWindows(binaryPath string) error {
	// Remove existing task if present (ignore errors)
	_ = exec.Command("schtasks", "/delete", "/tn", taskName, "/f").Run()

	// Create task: runs on logon + repeats every 8 hours indefinitely
	out, err := exec.Command("schtasks", "/create",
		"/tn", taskName,
		"/tr", fmt.Sprintf(`"%s" sync -p "%s"`, binaryPath, passwordFlag),
		"/sc", "ONLOGON",
		"/ri", "480", // repeat interval: 480 minutes = 8 hours
		"/du", "9999:59", // duration: effectively forever
		"/rl", "LIMITED",
		"/f",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create scheduled task: %s\n%s", err, string(out))
	}

	logger.Info("Scheduled task %q created successfully.", taskName)

	// Run it immediately
	out, err = exec.Command("schtasks", "/run", "/tn", taskName).CombinedOutput()
	if err != nil {
		logger.Warning("Task created but failed to start immediately: %s\n%s", err, string(out))
	} else {
		logger.Info("Task started immediately.")
	}

	return nil
}

func setupLinux(binaryPath string) error {
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get user config dir: %w", err)
	}
	unitDir := filepath.Join(userConfigDir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return fmt.Errorf("failed to create systemd user dir: %w", err)
	}

	servicePath := filepath.Join(unitDir, taskName+".service")
	timerPath := filepath.Join(unitDir, taskName+".timer")

	serviceContent := fmt.Sprintf(`[Unit]
Description=Cloud Drives Sync

[Service]
Type=oneshot
ExecStart=%s sync -p %s
`, binaryPath, passwordFlag)

	timerContent := fmt.Sprintf(`[Unit]
Description=Cloud Drives Sync Timer

[Timer]
OnBootSec=5min
OnUnitActiveSec=8h
Persistent=true

[Install]
WantedBy=timers.target
`)

	if err := os.WriteFile(servicePath, []byte(serviceContent), 0600); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}
	if err := os.WriteFile(timerPath, []byte(timerContent), 0644); err != nil {
		return fmt.Errorf("failed to write timer file: %w", err)
	}

	// Reload and enable
	cmds := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", taskName + ".timer"},
	}
	for _, c := range cmds {
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to run %s: %s\n%s", strings.Join(c, " "), err, string(out))
		}
	}

	logger.Info("Systemd timer %q enabled and started.", taskName)
	return nil
}

// ---------------------------------------------------------------------------
// Remove
// ---------------------------------------------------------------------------

func removeSchedule() error {
	switch runtime.GOOS {
	case "windows":
		return removeWindows()
	case "linux":
		return removeLinux()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func removeWindows() error {
	out, err := exec.Command("schtasks", "/delete", "/tn", taskName, "/f").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete scheduled task: %s\n%s", err, string(out))
	}
	logger.Info("Scheduled task %q removed.", taskName)
	return nil
}

func removeLinux() error {
	// Stop and disable
	_ = exec.Command("systemctl", "--user", "disable", "--now", taskName+".timer").Run()
	_ = exec.Command("systemctl", "--user", "stop", taskName+".service").Run()

	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get user config dir: %w", err)
	}
	unitDir := filepath.Join(userConfigDir, "systemd", "user")

	for _, name := range []string{taskName + ".service", taskName + ".timer"} {
		p := filepath.Join(unitDir, name)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			logger.Warning("Failed to remove %s: %v", p, err)
		}
	}

	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()

	logger.Info("Systemd timer %q removed.", taskName)
	return nil
}
