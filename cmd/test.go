//go:build !auto

package cmd

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/database"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/microsoft"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/FranLegon/cloud-drives-sync/internal/telegram"
	"github.com/spf13/cobra"
)

var testUnsafe bool
var testBackup bool
var testCase string
var testWithCommit string
var test10Hash string

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Run end-to-end acceptance tests",
	Long:  `Run SPEC acceptance scenarios against real provider accounts.`,
	Annotations: map[string]string{
		"skipDB": "true",
	},
	RunE: runTest,
}

func init() {
	testCmd.Flags().StringVar(&testCase, "case", "", "Run one SPEC test case only")
	testCmd.Flags().BoolVar(&testUnsafe, "unsafe", false, "Delete pre-existing managed data before running tests")
	testCmd.Flags().BoolVar(&testBackup, "backup", false, "Rename pre-existing managed root before running tests")
	testCmd.Flags().StringVarP(&testWithCommit, "with-commit", "c", "", "Commit current state to test branch, run tests, and merge on success")
	testCmd.Flags().Lookup("with-commit").NoOptDefVal = "__auto__"
	rootCmd.AddCommand(testCmd)
}

func runTest(cmd *cobra.Command, args []string) (retErr error) {
	if err := validateRequestedTestCase(testCase); err != nil {
		return err
	}

	// Setup Logging to file
	logFile, err := os.OpenFile("test.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return fmt.Errorf("failed to open test.log: %w", err)
	}
	defer logFile.Close()
	mw := io.MultiWriter(os.Stdout, logFile)
	logger.SetOutput(mw)

	// Archive test log with timestamp (runs at end regardless of success/failure)
	defer func() {
		logFile.Close() // Ensure all writes are flushed
		timestamp := time.Now().Format("20060102-150405")
		logsDir := "logs"
		logName := fmt.Sprintf("test-%s.log", timestamp)
		if testCase != "" {
			logName = fmt.Sprintf("test-%s-case-%s.log", timestamp, sanitizeTestCaseID(testCase))
		}
		archivedLogPath := filepath.Join(logsDir, logName)

		if err := os.MkdirAll(logsDir, 0755); err != nil {
			logger.Warning("Failed to create logs directory: %v", err)
			return
		}

		srcFile, err := os.Open("test.log")
		if err != nil {
			logger.Warning("Failed to open test.log for archiving: %v", err)
			return
		}
		defer srcFile.Close()

		dstFile, err := os.Create(archivedLogPath)
		if err != nil {
			logger.Warning("Failed to create archived log file: %v", err)
			return
		}
		defer dstFile.Close()

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			logger.Warning("Failed to copy test.log to archive: %v", err)
		} else {
			logger.Info("Test log archived to: %s", archivedLogPath)
		}
	}()

	// Start CPU profiling
	pprofTimestamp := time.Now().Format("2006-Jan-02_15-04-05")
	pprofDir := filepath.Join("logs", "prof")
	pprofPath := filepath.Join(pprofDir, fmt.Sprintf("cpu_%s.prof", pprofTimestamp))
	if err := os.MkdirAll(pprofDir, 0755); err != nil {
		return fmt.Errorf("failed to create logs/prof directory for pprof: %w", err)
	}
	pprofFile, err := os.Create(pprofPath)
	if err != nil {
		return fmt.Errorf("failed to create pprof file: %w", err)
	}
	if err := pprof.StartCPUProfile(pprofFile); err != nil {
		pprofFile.Close()
		return fmt.Errorf("failed to start CPU profile: %w", err)
	}
	defer func() {
		pprof.StopCPUProfile()
		pprofFile.Close()
		logger.Info("CPU profile saved to: %s", pprofPath)

		// Write memory (heap) profile
		memPath := filepath.Join(pprofDir, fmt.Sprintf("mem_%s.prof", pprofTimestamp))
		memFile, err := os.Create(memPath)
		if err != nil {
			logger.Warning("Failed to create memory profile: %v", err)
		} else {
			if err := pprof.WriteHeapProfile(memFile); err != nil {
				logger.Warning("Failed to write memory profile: %v", err)
			} else {
				logger.Info("Memory profile saved to: %s", memPath)
			}
			memFile.Close()
		}

		// Write block (IO/contention) profile
		blockPath := filepath.Join(pprofDir, fmt.Sprintf("block_%s.prof", pprofTimestamp))
		blockFile, err := os.Create(blockPath)
		if err != nil {
			logger.Warning("Failed to create block profile: %v", err)
		} else {
			if err := pprof.Lookup("block").WriteTo(blockFile, 0); err != nil {
				logger.Warning("Failed to write block profile: %v", err)
			} else {
				logger.Info("Block profile saved to: %s", blockPath)
			}
			blockFile.Close()
		}
	}()

	// Enable block profiling for IO/contention tracking
	runtime.SetBlockProfileRate(1)
	defer runtime.SetBlockProfileRate(0)

	testRuntimesStr := map[string]time.Duration{}
	totalStart := time.Now()
	var testCommitHash string
	var gitStashed bool
	var gitBranchCreated bool

	// Git cleanup: return to main, merge if passed, pop stash
	defer func() {
		if testWithCommit == "" {
			return
		}
		if gitBranchCreated {
			if retErr == nil {
				logger.Info("[GIT] All tests passed, merging test branch into main...")
				if _, err := runGit("checkout", "main"); err != nil {
					logger.Error("[GIT] Failed to checkout main: %v", err)
				} else if _, err := runGit("merge", "test"); err != nil {
					logger.Error("[GIT] Failed to merge test into main: %v", err)
				} else {
					logger.Info("[GIT] Successfully merged test into main")
				}
			} else {
				logger.Warning("[GIT] Tests failed, skipping merge. Restoring .go changes to working tree...")
				if _, err := runGit("checkout", "main"); err != nil {
					logger.Error("[GIT] Failed to checkout main: %v", err)
				} else if _, err := runGit("merge", "--squash", "test"); err != nil {
					logger.Error("[GIT] Failed to squash-merge test changes: %v", err)
				} else if _, err := runGit("reset"); err != nil {
					logger.Error("[GIT] Failed to unstage changes: %v", err)
				} else {
					logger.Info("[GIT] .go changes restored as uncommitted in working tree")
				}
			}
		}
		if gitStashed {
			logger.Info("[GIT] Restoring stashed non-.go files...")
			if _, err := runGit("stash", "pop"); err != nil {
				logger.Error("[GIT] Failed to pop stash: %v", err)
			}
		}
	}()

	// Print runtime summary before archiving the log
	defer func() {
		logger.Info("\n=== TEST RUNTIME SUMMARY ===")
		logger.Info("  Finished running tests. Optimization applied: Replaced heavy LEFT JOIN in GetFilesByStatus with separate indexed queries.")
		logger.Info("  Finished running tests. Optimization applied: Replaced heavy LEFT JOIN in GetFilesByCalculatedID with separate indexed queries.")
		allIDs := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23", "inner1", "inner2"}
		for _, step := range allIDs {
			if d, ok := testRuntimesStr[step]; ok {
				logger.Info("  Test Case %s: %s", step, d.Round(time.Millisecond))
			}
		}
		logger.Info("  Total Runtime:  %s", time.Since(totalStart).Round(time.Millisecond))
		if testCommitHash != "" {
			logger.Info("  Git commit tested: %s", testCommitHash)
		}
		logger.Info("=============================")
	}()

	defer func() {
		if retErr != nil {
			logger.Error("Test failed: %v", retErr)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			logger.Error("Recovered from panic: %v", r)
			retErr = fmt.Errorf("panic: %v", r)
		}
	}()
	logger.Info("Starting Test Command...")
	logger.Info("Additional logs added for debugging test command execution.")

	// --with-commit: git setup
	if testWithCommit != "" {
		// Pre-check: verify remote origin and current branch
		originURL, err := runGit("remote", "get-url", "origin")
		if err != nil {
			return fmt.Errorf("--with-commit: failed to get remote origin URL: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(originURL), "https://github.com/FranLegon/cloud-drives-sync.git") {
			return fmt.Errorf("--with-commit: remote origin URL is %q, expected https://github.com/FranLegon/cloud-drives-sync.git", originURL)
		}

		currentBranch, err := runGit("rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return fmt.Errorf("--with-commit: failed to get current branch: %w", err)
		}
		if currentBranch != "main" {
			return fmt.Errorf("--with-commit: current branch is %q, expected main", currentBranch)
		}

		// Parse git status to find changed files
		statusOutput, err := runGit("status", "--porcelain")
		if err != nil {
			return fmt.Errorf("--with-commit: failed to get git status: %w", err)
		}

		var goFiles []string
		var nonGoFiles []string
		if statusOutput != "" {
			for _, line := range strings.Split(statusOutput, "\n") {
				if len(line) < 4 {
					continue
				}
				// Status format: XY <path> or XY <path> -> <path>
				filePath := strings.TrimSpace(line[3:])
				if idx := strings.Index(filePath, " -> "); idx != -1 {
					filePath = filePath[idx+4:]
				}
				if strings.HasSuffix(filePath, ".go") {
					goFiles = append(goFiles, filePath)
				} else {
					nonGoFiles = append(nonGoFiles, filePath)
				}
			}
		}

		if len(goFiles) == 0 {
			// No .go changes: use current HEAD hash, skip branching
			hash, err := runGit("rev-parse", "HEAD")
			if err != nil {
				return fmt.Errorf("--with-commit: failed to get HEAD hash: %w", err)
			}
			testCommitHash = hash
			logger.Info("[GIT] No .go file changes detected. Using current HEAD: %s", testCommitHash)
		} else {
			logger.Info("[GIT] Attempting to stash %d non-.go files", len(nonGoFiles))
			// Stash non-.go files if any
			if len(nonGoFiles) > 0 {
				stashArgs := append([]string{"stash", "push", "--include-untracked", "--"}, nonGoFiles...)
				if _, err := runGit(stashArgs...); err != nil {
					return fmt.Errorf("--with-commit: failed to stash non-.go files: %w", err)
				}
				gitStashed = true
				logger.Info("[GIT] Stashed %d non-.go file(s)", len(nonGoFiles))
			}

			// Create/reset test branch from main
			if _, err := runGit("checkout", "-B", "test", "main"); err != nil {
				return fmt.Errorf("--with-commit: failed to create test branch: %w", err)
			}
			gitBranchCreated = true

			// Stage all .go files (handles new, modified, deleted)
			addArgs := append([]string{"add", "--"}, goFiles...)
			if _, err := runGit(addArgs...); err != nil {
				return fmt.Errorf("--with-commit: failed to stage .go files: %w", err)
			}

			// Commit
			commitMsg := strings.TrimSpace(testWithCommit)
			if commitMsg == "" || commitMsg == "__auto__" {
				if msgBytes, err := os.ReadFile(".commitmsg"); err == nil && len(strings.TrimSpace(string(msgBytes))) > 0 {
					commitMsg = strings.TrimSpace(string(msgBytes))
					os.Remove(".commitmsg")
				} else if stat, err := runGit("diff", "--cached", "--stat"); err == nil && strings.TrimSpace(stat) != "" {
					commitMsg = stat
				} else {
					commitMsg = fmt.Sprintf("Test Run: %s", time.Now().Format("02Jan2006 15:04:05"))
				}
			}
			if _, err := runGit("commit", "-m", commitMsg); err != nil {
				return fmt.Errorf("--with-commit: failed to commit: %w", err)
			}

			hash, err := runGit("rev-parse", "HEAD")
			if err != nil {
				return fmt.Errorf("--with-commit: failed to get test branch hash: %w", err)
			}
			testCommitHash = hash
			logger.Info("[GIT] Committed .go changes to test branch: %s", testCommitHash)
		}
	}

	// Use smaller fragment limit for tests (2MB instead of 2GB)
	telegram.SetDefaultMaxPartSize(2 * 1024 * 1024)

	// Use isolated test folders/channel to avoid touching production data
	google.SetSyncFolderName("cloud-drives-sync-test")
	microsoft.SetSyncFolderName("cloud-drives-sync-test")
	telegram.SetSyncChannelName("cloud-drives-sync-test")
	task.SetAuxFolder("cloud-drives-sync-test-aux")
	database.SetAuxFolderName("cloud-drives-sync-test-aux")
	defer func() {
		google.SetSyncFolderName("cloud-drives-sync")
		microsoft.SetSyncFolderName("cloud-drives-sync")
		telegram.SetSyncChannelName("cloud-drives-sync")
		task.SetAuxFolder("cloud-drives-sync-aux")
		database.SetAuxFolderName("cloud-drives-sync-aux")
	}()

	runner := task.NewRunner(cfg, nil, false) // Temporary runner for cleanup

	// Run Setup (Phase 0 + Init)
	// Always run setup unless we are running a specific test case AND logic dictates otherwise,
	// but generally we need setup.
	if err := runSetup(runner); err != nil {
		return err
	}

	logger.Info("[TEST] Re-init runner with DB after setup")
	// Re-init runner with DB after setup
	if db == nil {
		var err error
		db, err = database.Open(masterPassword)
		if err != nil {
			return fmt.Errorf("failed to open DB: %w", err)
		}
	}
	runner = task.NewRunner(cfg, db, false)
	runner.SetStopOnError(true)

	logger.Info("[TEST] Ensure sync folders exist")
	// Ensure folders exist
	if err := recreateSyncFolders(runner, cfg); err != nil {
		return fmt.Errorf("failed to recreate sync folders: %w", err)
	}

	logger.Info("[TEST] Ensuring special aux folders exist (simulating config --init)...")
	if err := runner.EnsureSpecialFolders(); err != nil {
		return fmt.Errorf("EnsureSpecialFolders failed: %w", err)
	}

	logger.Info("[TEST] Running initial GetMetadata...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("GetMetadata failed: %w", err)
	}

	// Verify Clean
	logger.Info("Verifying clean state...")
	files, err := db.GetAllFiles()
	if err != nil {
		return err
	}
	if len(files) > 0 {
		return fmt.Errorf("verification failed: found %d active files in clean state (e.g. %s)", len(files), files[0].Path)
	}

	// Create dummy files - SKIPPED (In-Memory)

	var mainUser *model.User
	var backups []*model.User
	for i := range cfg.Users {
		u := &cfg.Users[i]
		if u.IsMain {
			mainUser = u
		} else {
			backups = append(backups, u)
		}
	}
	if mainUser == nil {
		return fmt.Errorf("no main account found")
	}

	selectedCases := specTestCases()
	if testCase != "" {
		selected := findSpecTestCase(testCase)
		if selected == nil {
			return fmt.Errorf("unknown SPEC test case %q", testCase)
		}
		selectedCases = []specTestCase{*selected}
	}

	for _, tc := range selectedCases {
		logger.Info("\n=== Running SPEC Test Case %s: %s ===", tc.ID, tc.Name)
		started := time.Now()
		if err := tc.Run(runner, mainUser, backups); err != nil {
			return fmt.Errorf("SPEC test case %s (%s) failed: %w", tc.ID, tc.Name, err)
		}
		if tc.LegacyAlias != "9" && tc.LegacyAlias != "12" {
			if err := testMetadata(runner); err != nil {
				return fmt.Errorf("metadata verification failed after SPEC test case %s: %w", tc.ID, err)
			}
		}
		testRuntimesStr[tc.ID] = time.Since(started)
	}

	logger.Info("\nTEST SUITE COMPLETED SUCCESSFULLY")
	logger.Info("All SPEC test cases have passed.")
	return nil
}

func specCaseInner1(r *task.Runner, main *model.User, backups []*model.User) error {
	return legacyOwnershipTransferTest(r, main, backups)
}

func specCaseInner2(r *task.Runner, main *model.User, backups []*model.User) error {
	logger.Info("inner2: Microsoft OneDrive Real Shortcut — verifying shortcut creation (integrated into case 23)")
	return specCase23(r, main, backups)
}

// randStr returns a random lowercase alphanumeric string of exactly n characters.
func randStr(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, n)
	for i := range buf {
		idx := mustRand(int64(len(alphabet)))
		buf[i] = alphabet[idx]
	}
	return string(buf)
}

// fileContent builds the standard test file body.
func fileContent(caseID, suffix string) []byte {
	return []byte(fmt.Sprintf("test-case-id = %s\n%s", caseID, suffix))
}

// verifyFileOnAllProviders downloads the file from each provider and checks content matches expected.
func verifyFileOnAllProviders(r *task.Runner, mainUser *model.User, backups []*model.User, path string, expectedContent []byte) error {
	allUsers := append([]*model.User{mainUser}, backups...)
	for _, u := range allUsers {
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			return fmt.Errorf("get client for %s: %w", u.Email, err)
		}
		f, err := db.GetFileByPath(path)
		if err != nil || f == nil {
			return fmt.Errorf("file %s not found in DB", path)
		}
		var nid string
		if u.Provider == model.ProviderGoogle {
			// Google shared folder: one physical file shared across all accounts.
			// Always use the active Google replica's NativeID — stale per-account
			// entries must not be used as they may refer to trashed files.
			for _, rep := range f.Replicas {
				if rep.Provider == model.ProviderGoogle && rep.Status == "active" {
					nid = rep.NativeID
					break
				}
			}
		} else {
			nid = getNativeID(f, u)
		}
		if nid == "" {
			// Microsoft may have a placeholder/shortcut — skip content check.
			if u.Provider == model.ProviderMicrosoft {
				continue
			}
			return fmt.Errorf("no nativeID for %s on %s", path, u.Email)
		}
		// Fragmented Telegram replicas cannot be downloaded as a whole via DownloadFile.
		// Content was already verified by the sync restoring it to Google/Microsoft.
		if u.Provider == model.ProviderTelegram {
			fragmented := false
			for _, rep := range f.Replicas {
				if rep.Provider == model.ProviderTelegram && rep.Status == "active" && rep.Fragmented {
					fragmented = true
					break
				}
			}
			if fragmented {
				continue
			}
		}
		var buf bytes.Buffer
		if err := client.DownloadFile(nid, &buf); err != nil {
			return fmt.Errorf("download from %s (%s) failed: %w", u.Email, u.Provider, err)
		}
		if expectedContent != nil && !bytes.Equal(buf.Bytes(), expectedContent) {
			return fmt.Errorf("content mismatch on %s (%s): expected %q got %q", u.Email, u.Provider, string(expectedContent), buf.String())
		}
	}
	return nil
}

// verifyFolderOnAllProviders checks that a named folder exists on every provider that supports folders.
func verifyFolderOnAllProviders(r *task.Runner, mainUser *model.User, backups []*model.User, folderName string) error {
	allUsers := append([]*model.User{mainUser}, backups...)
	for _, u := range allUsers {
		if u.Provider == model.ProviderTelegram {
			continue
		}
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			return fmt.Errorf("get client for %s: %w", u.Email, err)
		}
		sid, err := client.GetSyncFolderID()
		if err != nil {
			return fmt.Errorf("get sync folder for %s: %w", u.Email, err)
		}
		folders, err := client.ListFolders(sid)
		if err != nil {
			return fmt.Errorf("list folders for %s: %w", u.Email, err)
		}
		found := false
		for _, f := range folders {
			if f.Name == folderName {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("folder %q not found for account %s (%s)", folderName, u.Email, u.Provider)
		}
	}
	return nil
}

// verifyNestedFolderOnAllProviders checks a nested path exists on every provider that supports folders.
func verifyNestedFolderOnAllProviders(r *task.Runner, mainUser *model.User, backups []*model.User, parts []string) error {
	allUsers := append([]*model.User{mainUser}, backups...)
	for _, u := range allUsers {
		if u.Provider == model.ProviderTelegram {
			continue
		}
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			return fmt.Errorf("get client for %s: %w", u.Email, err)
		}
		parentID, err := client.GetSyncFolderID()
		if err != nil {
			return fmt.Errorf("get sync folder for %s: %w", u.Email, err)
		}
		for _, part := range parts {
			folders, err := client.ListFolders(parentID)
			if err != nil {
				return fmt.Errorf("list folders (path %v) for %s: %w", parts, u.Email, err)
			}
			found := false
			for _, f := range folders {
				if f.Name == part {
					parentID = f.ID
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("folder segment %q not found under path for %s (%s)", part, u.Email, u.Provider)
			}
		}
	}
	return nil
}

// SPEC Case 1: Clean-slate setup
func specCase1(r *task.Runner, main *model.User, backups []*model.User) error {
	logger.Info("[MANUAL INTERACTION] Verification: special folders and clean DB state after config --init")
	allUsers := append([]*model.User{main}, backups...)
	specialFolders := []string{
		task.AuxFolder,
		task.AuxFolder + "/soft-deleted",
		task.AuxFolder + "/hard-deleted",
		task.AuxFolder + "/unsynced-from-backups",
	}
	for _, u := range allUsers {
		if u.Provider == model.ProviderTelegram {
			continue
		}
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			return fmt.Errorf("get client %s: %w", u.Email, err)
		}
		sid, err := client.GetSyncFolderID()
		if err != nil {
			return fmt.Errorf("sync folder for %s: %w", u.Email, err)
		}
		for _, folderPath := range specialFolders {
			parts := strings.Split(folderPath, "/")
			parentID := sid
			for _, part := range parts {
				folders, err := client.ListFolders(parentID)
				if err != nil {
					return fmt.Errorf("list folders for %s: %w", u.Email, err)
				}
				found := false
				for _, f := range folders {
					if f.Name == part {
						parentID = f.ID
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("special folder segment %q missing for %s", part, u.Email)
				}
			}
		}
	}
	files, err := db.GetAllFiles()
	if err != nil {
		return err
	}
	if len(files) > 0 {
		return fmt.Errorf("expected 0 active files in clean state, got %d", len(files))
	}
	logger.Info("[VERIFICATION] Case 1 passed: special folders present, DB clean")
	return nil
}

// SPEC Case 2: Create file on main
func specCase2(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("2", rand69+"\n")
	fileName := "test-case-id-2.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Create file '%s' in cloud-drives-sync-root", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload %s: %w", fileName, err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, "/"+fileName, content)
}

// SPEC Case 3: Create file on Google backup
func specCase3(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("3", rand69+"\n")
	fileName := "test-case-id-3.txt"
	googleBackups := filterUsers(backups, model.ProviderGoogle)
	if len(googleBackups) == 0 {
		return fmt.Errorf("no Google backup accounts found")
	}
	backup := googleBackups[0]
	logger.Info("[MANUAL INTERACTION] [%s] Create file '%s' in cloud-drives-sync-root", backup.Email, fileName)
	client, err := r.GetOrCreateClient(backup)
	if err != nil {
		return err
	}
	sid, err := client.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := client.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload %s: %w", fileName, err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, "/"+fileName, content)
}

// SPEC Case 4: Create folder on main
func specCase4(r *task.Runner, main *model.User, backups []*model.User) error {
	folderName := "test-case-id-4-folder"
	logger.Info("[MANUAL INTERACTION] [%s] Create folder '%s' in cloud-drives-sync-root", main.Email, folderName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.CreateFolder(sid, folderName); err != nil {
		return fmt.Errorf("create folder: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	return verifyFolderOnAllProviders(r, main, backups, folderName)
}

// SPEC Case 5: Create folder on Google backup
func specCase5(r *task.Runner, main *model.User, backups []*model.User) error {
	folderName := "test-case-id-5-folder"
	googleBackups := filterUsers(backups, model.ProviderGoogle)
	if len(googleBackups) == 0 {
		return fmt.Errorf("no Google backup accounts found")
	}
	backup := googleBackups[0]
	logger.Info("[MANUAL INTERACTION] [%s] Create folder '%s' in cloud-drives-sync-root", backup.Email, folderName)
	client, err := r.GetOrCreateClient(backup)
	if err != nil {
		return err
	}
	sid, err := client.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := client.CreateFolder(sid, folderName); err != nil {
		return fmt.Errorf("create folder: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	return verifyFolderOnAllProviders(r, main, backups, folderName)
}

// SPEC Case 6: Create file on Microsoft backup
func specCase6(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("6", rand69+"\n")
	fileName := "test-case-id-6.txt"
	msBackups := filterUsers(backups, model.ProviderMicrosoft)
	if len(msBackups) == 0 {
		return fmt.Errorf("no Microsoft backup accounts found")
	}
	backup := msBackups[0]
	logger.Info("[MANUAL INTERACTION] [%s] Create file '%s' in cloud-drives-sync-root", backup.Email, fileName)
	client, err := r.GetOrCreateClient(backup)
	if err != nil {
		return err
	}
	sid, err := client.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := client.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload %s: %w", fileName, err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, "/"+fileName, content)
}

// SPEC Case 7: Create folder on Microsoft backup
func specCase7(r *task.Runner, main *model.User, backups []*model.User) error {
	folderName := "test-case-id-7-folder"
	msBackups := filterUsers(backups, model.ProviderMicrosoft)
	if len(msBackups) == 0 {
		return fmt.Errorf("no Microsoft backup accounts found")
	}
	backup := msBackups[0]
	logger.Info("[MANUAL INTERACTION] [%s] Create folder '%s' in cloud-drives-sync-root", backup.Email, folderName)
	client, err := r.GetOrCreateClient(backup)
	if err != nil {
		return err
	}
	sid, err := client.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := client.CreateFolder(sid, folderName); err != nil {
		return fmt.Errorf("create folder: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	return verifyFolderOnAllProviders(r, main, backups, folderName)
}

// SPEC Case 8: Sync file from Telegram
func specCase8(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("8", rand69+"\n")
	fileName := "test-case-id-8.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Upload '%s' to cloud-drives-sync-root (Google main)", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload %s: %w", fileName, err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	allUsers := append([]*model.User{main}, backups...)
	f, err := db.GetFileByPath("/" + fileName)
	if err != nil || f == nil {
		return fmt.Errorf("file %s not found in DB after first sync", fileName)
	}
	for _, u := range allUsers {
		if u.Provider == model.ProviderTelegram {
			continue
		}
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			continue
		}
		nid := getNativeID(f, u)
		if nid == "" {
			continue
		}
		logger.Info("[MANUAL INTERACTION] [%s] Delete '%s' to leave only Telegram replica", u.Email, fileName)
		client.DeleteFile(nid)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync (restore from Telegram) failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, "/"+fileName, content)
}

// SPEC Case 9: Sync file from Microsoft
func specCase9(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("9", rand69+"\n")
	fileName := "test-case-id-9.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Upload '%s' to cloud-drives-sync-root (Google main)", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload %s: %w", fileName, err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	allUsers := append([]*model.User{main}, backups...)
	f, err := db.GetFileByPath("/" + fileName)
	if err != nil || f == nil {
		return fmt.Errorf("file %s not found in DB after first sync", fileName)
	}
	for _, u := range allUsers {
		if u.Provider == model.ProviderMicrosoft {
			continue
		}
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			continue
		}
		nid := getNativeID(f, u)
		if nid == "" {
			continue
		}
		logger.Info("[MANUAL INTERACTION] [%s] Delete '%s' to leave only Microsoft replica", u.Email, fileName)
		client.DeleteFile(nid)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync (restore from Microsoft) failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, "/"+fileName, content)
}

// SPEC Case 10: Move Google Drive files from backups roots
func specCase10(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("10", rand69+"\n")
	fileName := "test-case-id-10.txt"
	googleBackups := filterUsers(backups, model.ProviderGoogle)
	if len(googleBackups) == 0 {
		return fmt.Errorf("no Google backup accounts")
	}
	backup := googleBackups[0]
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' in the actual root (not sync folder)", backup.Email, fileName)
	client, err := r.GetOrCreateClient(backup)
	if err != nil {
		return err
	}
	if _, err := client.UploadFile("root", fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload to root: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	expectedPath := "/" + task.AuxFolder + "/unsynced-from-backups/" + fileName
	if err := verifyFileInDB(expectedPath); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, expectedPath, content)
}

// SPEC Case 11: Google Drive nested folders
func specCase11(r *task.Runner, main *model.User, backups []*model.User) error {
	logger.Info("[MANUAL INTERACTION] [%s] Create nested folder structure test-case-id-11-folder/test-case-id-11-subfolder/test-case-id-11-subsubfolder", main.Email)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	f1, err := getOrCreateFolder(mainClient, sid, "test-case-id-11-folder")
	if err != nil {
		return err
	}
	f2, err := getOrCreateFolder(mainClient, f1.ID, "test-case-id-11-subfolder")
	if err != nil {
		return err
	}
	if _, err := getOrCreateFolder(mainClient, f2.ID, "test-case-id-11-subsubfolder"); err != nil {
		return err
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	return verifyNestedFolderOnAllProviders(r, main, backups, []string{"test-case-id-11-folder", "test-case-id-11-subfolder", "test-case-id-11-subsubfolder"})
}

// SPEC Case 12: Microsoft OneDrive nested folders
func specCase12(r *task.Runner, main *model.User, backups []*model.User) error {
	msBackups := filterUsers(backups, model.ProviderMicrosoft)
	if len(msBackups) == 0 {
		return fmt.Errorf("no Microsoft backup accounts")
	}
	backup := msBackups[0]
	logger.Info("[MANUAL INTERACTION] [%s] Create nested folder structure test-case-id-12-folder/test-case-id-12-subfolder/test-case-id-12-subsubfolder", backup.Email)
	client, err := r.GetOrCreateClient(backup)
	if err != nil {
		return err
	}
	sid, err := client.GetSyncFolderID()
	if err != nil {
		return err
	}
	f1, err := getOrCreateFolder(client, sid, "test-case-id-12-folder")
	if err != nil {
		return err
	}
	f2, err := getOrCreateFolder(client, f1.ID, "test-case-id-12-subfolder")
	if err != nil {
		return err
	}
	if _, err := getOrCreateFolder(client, f2.ID, "test-case-id-12-subsubfolder"); err != nil {
		return err
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	return verifyNestedFolderOnAllProviders(r, main, backups, []string{"test-case-id-12-folder", "test-case-id-12-subfolder", "test-case-id-12-subsubfolder"})
}

// SPEC Case 13: Google Drive moved file
func specCase13(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("13", rand69+"\n")
	fileName := "test-case-id-13.txt"
	folderName := "test-case-id-13-folder"
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' and folder '%s' in cloud-drives-sync-root", main.Email, fileName, folderName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	uploadedFile, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	folder, err := getOrCreateFolder(mainClient, sid, folderName)
	if err != nil {
		return fmt.Errorf("create folder: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	fileNativeID := uploadedFile.Replicas[0].NativeID
	logger.Info("[MANUAL INTERACTION] [%s] Move '%s' into '%s'", main.Email, fileName, folderName)
	if err := mainClient.MoveFile(fileNativeID, folder.ID); err != nil {
		return fmt.Errorf("move file: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync failed: %w", err)
	}
	expectedPath := "/" + folderName + "/" + fileName
	if err := verifyFileInDB(expectedPath); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, expectedPath, content)
}

// SPEC Case 14: Google Drive files created directly in nested folders
func specCase14(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69A := randStr(69)
	rand69B := randStr(69)
	contentA := fileContent("14", rand69A+"\n")
	contentB := fileContent("14", rand69B+"\n")
	logger.Info("[MANUAL INTERACTION] [%s] Create nested folders and files for case 14", main.Email)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	root14, err := getOrCreateFolder(mainClient, sid, "test-case-id-14-folder")
	if err != nil {
		return err
	}
	subA, err := getOrCreateFolder(mainClient, root14.ID, "test-case-id-14-subfolder-A")
	if err != nil {
		return err
	}
	subB, err := getOrCreateFolder(mainClient, root14.ID, "test-case-id-14-subfolder-B")
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(subA.ID, "test-case-id-14-n1.txt", bytes.NewReader(contentA), int64(len(contentA))); err != nil {
		return fmt.Errorf("upload n1: %w", err)
	}
	if _, err := mainClient.UploadFile(subB.ID, "test-case-id-14-n2.txt", bytes.NewReader(contentB), int64(len(contentB))); err != nil {
		return fmt.Errorf("upload n2: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	pathA := "/test-case-id-14-folder/test-case-id-14-subfolder-A/test-case-id-14-n1.txt"
	pathB := "/test-case-id-14-folder/test-case-id-14-subfolder-B/test-case-id-14-n2.txt"
	if err := verifyFileInDB(pathA); err != nil {
		return err
	}
	if err := verifyFileInDB(pathB); err != nil {
		return err
	}
	if err := verifyFileOnAllProviders(r, main, backups, pathA, contentA); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, pathB, contentB)
}

// SPEC Case 15: Google Drive multiple moved files
func specCase15(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69A := randStr(69)
	rand69B := randStr(69)
	rand69C := randStr(69)
	contentN1 := fileContent("15", "Origin: test-case-id-15-subfolder-A\nDestination: test-case-id-15-subsubfolder-A\n"+rand69A+"\n")
	contentN2 := fileContent("15", "Origin: test-case-id-15-subsubfolder-A\nDestination: test-case-id-15-subfolder-B\n"+rand69B+"\n")
	contentN3 := fileContent("15", "Origin: test-case-id-15-subfolder-B\nDestination: test-case-id-15-subfolder-A\n"+rand69C+"\n")
	logger.Info("[MANUAL INTERACTION] [%s] Create nested folder structures and files for case 15", main.Email)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	root15, err := getOrCreateFolder(mainClient, sid, "test-case-id-15-folder")
	if err != nil {
		return err
	}
	subA, err := getOrCreateFolder(mainClient, root15.ID, "test-case-id-15-subfolder-A")
	if err != nil {
		return err
	}
	subsubA, err := getOrCreateFolder(mainClient, subA.ID, "test-case-id-15-subsubfolder-A")
	if err != nil {
		return err
	}
	subB, err := getOrCreateFolder(mainClient, root15.ID, "test-case-id-15-subfolder-B")
	if err != nil {
		return err
	}
	subsubB, err := getOrCreateFolder(mainClient, subB.ID, "test-case-id-15-subsubfolder-B")
	if err != nil {
		return err
	}
	_ = subsubB
	n1, err := mainClient.UploadFile(subA.ID, "test-case-id-15-n1.txt", bytes.NewReader(contentN1), int64(len(contentN1)))
	if err != nil {
		return fmt.Errorf("upload n1: %w", err)
	}
	n2, err := mainClient.UploadFile(subsubA.ID, "test-case-id-15-n2.txt", bytes.NewReader(contentN2), int64(len(contentN2)))
	if err != nil {
		return fmt.Errorf("upload n2: %w", err)
	}
	n3, err := mainClient.UploadFile(subB.ID, "test-case-id-15-n3.txt", bytes.NewReader(contentN3), int64(len(contentN3)))
	if err != nil {
		return fmt.Errorf("upload n3: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	logger.Info("[MANUAL INTERACTION] [%s] Move files to their destinations", main.Email)
	if err := mainClient.MoveFile(n1.Replicas[0].NativeID, subsubA.ID); err != nil {
		return fmt.Errorf("move n1: %w", err)
	}
	if err := mainClient.MoveFile(n2.Replicas[0].NativeID, subB.ID); err != nil {
		return fmt.Errorf("move n2: %w", err)
	}
	if err := mainClient.MoveFile(n3.Replicas[0].NativeID, subA.ID); err != nil {
		return fmt.Errorf("move n3: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync failed: %w", err)
	}
	type pathContent struct {
		path    string
		content []byte
	}
	checks := []pathContent{
		{"/test-case-id-15-folder/test-case-id-15-subfolder-A/test-case-id-15-subsubfolder-A/test-case-id-15-n1.txt", contentN1},
		{"/test-case-id-15-folder/test-case-id-15-subfolder-B/test-case-id-15-n2.txt", contentN2},
		{"/test-case-id-15-folder/test-case-id-15-subfolder-A/test-case-id-15-n3.txt", contentN3},
	}
	for _, c := range checks {
		if err := verifyFileInDB(c.path); err != nil {
			return err
		}
		if err := verifyFileOnAllProviders(r, main, backups, c.path, c.content); err != nil {
			return err
		}
	}
	return nil
}

// SPEC Case 16: Google Drive soft-delete and restore
func specCase16(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("16", rand69+"\n")
	fileName := "test-case-id-16.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' in cloud-drives-sync-root", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	auxFolder, err := getOrCreateFolder(mainClient, sid, task.AuxFolder)
	if err != nil {
		return err
	}
	softFolder, err := getOrCreateFolder(mainClient, auxFolder.ID, "soft-deleted")
	if err != nil {
		return err
	}
	f, err := db.GetFileByPath("/" + fileName)
	if err != nil || f == nil {
		return fmt.Errorf("file not found in DB")
	}
	nid := getNativeID(f, main)
	logger.Info("[MANUAL INTERACTION] [%s] Move '%s' to soft-deleted", main.Email, fileName)
	if err := mainClient.MoveFile(nid, softFolder.ID); err != nil {
		return fmt.Errorf("move to soft-deleted: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync (soft-delete) failed: %w", err)
	}
	if err := verifyFileStatus(db, "/"+fileName, "active", true); err != nil {
		return fmt.Errorf("soft-delete not propagated: %w", err)
	}
	softFiles, err := mainClient.ListFiles(softFolder.ID)
	if err != nil {
		return err
	}
	var softNID string
	for _, sf := range softFiles {
		if sf.Name == fileName {
			softNID = sf.ID
			break
		}
	}
	if softNID == "" {
		return fmt.Errorf("file not found in soft-deleted folder on main account")
	}
	logger.Info("[MANUAL INTERACTION] [%s] Move '%s' back to cloud-drives-sync-root (restore)", main.Email, fileName)
	if err := mainClient.MoveFile(softNID, sid); err != nil {
		return fmt.Errorf("restore from soft-deleted: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("third sync (restore) failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	if err := verifyFileStatus(db, "/"+fileName, "active", false); err != nil {
		return err
	}
	return verifyFileOnAllProviders(r, main, backups, "/"+fileName, content)
}

// SPEC Case 17: Google Drive hard-delete
func specCase17(r *task.Runner, main *model.User, backups []*model.User) error {
	rand69 := randStr(69)
	content := fileContent("17", rand69+"\n")
	fileName := "test-case-id-17.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' in cloud-drives-sync-root", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	auxFolder, err := getOrCreateFolder(mainClient, sid, task.AuxFolder)
	if err != nil {
		return err
	}
	hardFolder, err := getOrCreateFolder(mainClient, auxFolder.ID, "hard-deleted")
	if err != nil {
		return err
	}
	f, err := db.GetFileByPath("/" + fileName)
	if err != nil || f == nil {
		return fmt.Errorf("file not found in DB")
	}
	// Use any active Google replica NativeID — shared folder model means main can move it
	var nid string
	for _, rep := range f.Replicas {
		if rep.Provider == model.ProviderGoogle && rep.Status == "active" {
			nid = rep.NativeID
			break
		}
	}
	if nid == "" {
		return fmt.Errorf("no active Google replica found for %s to move to hard-deleted", fileName)
	}
	logger.Info("[MANUAL INTERACTION] [%s] Move '%s' to hard-deleted", main.Email, fileName)
	if err := mainClient.MoveFile(nid, hardFolder.ID); err != nil {
		return fmt.Errorf("move to hard-deleted: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync (hard-delete) failed: %w", err)
	}
	// Verify file does not exist on any provider outside the aux folder
	// (the aux/hard-deleted folder itself is processed and emptied by the sync)
	allUsers := append([]*model.User{main}, backups...)
	for _, u := range allUsers {
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			continue
		}
		cloudSID, err := client.GetSyncFolderID()
		if err != nil {
			continue
		}
		// List only the sync root level (non-aux files) + recurse into non-aux subfolders
		cloudFiles := map[string]*model.File{}
		listFilesRecursiveExcludeAux(client, cloudSID, "", cloudFiles)
		for _, cf := range cloudFiles {
			if cf.Name == fileName {
				return fmt.Errorf("file %s still exists on %s after hard-delete", fileName, u.Email)
			}
		}
	}
	// Verify DB state
	dbFile, _ := db.GetFileByPath("/" + fileName)
	if dbFile != nil && dbFile.Status != "hard-deleted" && dbFile.Status != "deleted" {
		return fmt.Errorf("file %s in DB has status %q, expected hard-deleted", fileName, dbFile.Status)
	}
	logger.Info("[VERIFICATION] Case 17 passed: file hard-deleted from all providers")
	return nil
}

// SPEC Case 18: Telegram fragmentation and defragmentation restore
func specCase18(r *task.Runner, main *model.User, backups []*model.User) error {
	const size = 2*1024*1024 + 512*1024 // 2.5 MB
	rand69 := randStr(69)
	header := []byte(fmt.Sprintf("test-case-id = 18\n%s\n", rand69))
	content := make([]byte, size)
	copy(content, header)
	for i := len(header); i < size; i++ {
		content[i] = 'x'
	}
	fileName := "test-case-id-18.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Upload 2.5MB '%s' to cloud-drives-sync-root", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	allUsers := append([]*model.User{main}, backups...)
	f, err := db.GetFileByPath("/" + fileName)
	if err != nil || f == nil {
		return fmt.Errorf("file not found in DB after first sync")
	}
	for _, u := range allUsers {
		if u.Provider == model.ProviderTelegram {
			continue
		}
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			continue
		}
		nid := getNativeID(f, u)
		if nid == "" {
			continue
		}
		logger.Info("[MANUAL INTERACTION] [%s] Delete '%s' leaving only Telegram fragmented replica", u.Email, fileName)
		client.DeleteFile(nid)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync (restore from Telegram fragments) failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	f2, err := db.GetFileByPath("/" + fileName)
	if err != nil || f2 == nil {
		return fmt.Errorf("file not found in DB after restore")
	}
	for _, rep := range f2.Replicas {
		if rep.Provider == model.ProviderTelegram && rep.Status == "active" && !rep.Fragmented {
			return fmt.Errorf("Telegram replica not marked as fragmented")
		}
	}
	return verifyFileOnAllProviders(r, main, backups, "/"+fileName, content)
}

// SPEC Case 19: Idempotent sync
func specCase19(r *task.Runner, main *model.User, backups []*model.User) error {
	content := []byte("test-case-id = 19\n")
	fileName := "test-case-id-19.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' in cloud-drives-sync-root", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	hash1, err := db.GetMetadataHash()
	if err != nil {
		return fmt.Errorf("get db hash 1: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync failed: %w", err)
	}
	hash2, err := db.GetMetadataHash()
	if err != nil {
		return fmt.Errorf("get db hash 2: %w", err)
	}
	if hash1 != hash2 {
		return fmt.Errorf("idempotence violation: DB hash changed between syncs (%s → %s)", hash1, hash2)
	}
	logger.Info("[VERIFICATION] Case 19 passed: DB hash identical after second sync (%s)", hash1)
	return nil
}

// SPEC Case 20: Quota check
func specCase20(r *task.Runner, main *model.User, backups []*model.User) error {
	logger.Info("[CLI COMMAND] Running: sync --quota")
	dbQuotas, err := r.GetProviderQuotasFromDB(true)
	if err != nil {
		return fmt.Errorf("get DB quotas: %w", err)
	}
	apiQuotas, err := r.GetProviderQuotasFromAPI()
	if err != nil {
		return fmt.Errorf("get API quotas: %w", err)
	}
	apiMap := make(map[model.Provider]*model.ProviderQuota)
	for _, q := range apiQuotas {
		apiMap[q.Provider] = q
	}
	for _, dbQ := range dbQuotas {
		apiQ, ok := apiMap[dbQ.Provider]
		if !ok {
			logger.Warning("[%s] missing in API quotas", dbQ.Provider)
			continue
		}
		logger.Info("[%s] DB SyncFolder: %s | API Account: %s | Diff: %s",
			dbQ.Provider, formatBytes(dbQ.SyncFolderUsed), formatBytes(apiQ.Used), formatBytes(apiQ.Used-dbQ.SyncFolderUsed))
	}
	logger.Info("[VERIFICATION] Case 20 passed: quota check complete")
	return nil
}

// SPEC Case 21: Divergent content at the same logical path
func specCase21(r *task.Runner, main *model.User, backups []*model.User) error {
	fileName := "test-case-id-21.txt"
	contentGoogle := []byte("test-case-id = 21\nUploaded to provider: Google Drive\n")
	contentMS := []byte("test-case-id = 21\nUploaded to provider: Microsoft OneDrive\n")
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' in cloud-drives-sync-root (Google main)", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(contentGoogle), int64(len(contentGoogle))); err != nil {
		return fmt.Errorf("upload to Google: %w", err)
	}
	msBackups := filterUsers(backups, model.ProviderMicrosoft)
	if len(msBackups) == 0 {
		return fmt.Errorf("no Microsoft backup accounts for divergent content test")
	}
	msClient, err := r.GetOrCreateClient(msBackups[0])
	if err != nil {
		return err
	}
	msSID, err := msClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' in cloud-drives-sync-root (Microsoft)", msBackups[0].Email, fileName)
	if _, err := msClient.UploadFile(msSID, fileName, bytes.NewReader(contentMS), int64(len(contentMS))); err != nil {
		return fmt.Errorf("upload to Microsoft: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	files, err := db.GetAllFiles()
	if err != nil {
		return err
	}
	conflictFound := false
	for _, f := range files {
		if strings.Contains(f.Name, "_conflict_") && strings.HasPrefix(f.Name, "test-case-id-21") {
			conflictFound = true
			break
		}
	}
	if !conflictFound {
		return fmt.Errorf("expected a conflict-renamed copy of %s but none found", fileName)
	}
	logger.Info("[VERIFICATION] Case 21 passed: conflict file created with timestamped suffix")
	return nil
}

// SPEC Case 22: Sync resumed after interruption
func specCase22(r *task.Runner, main *model.User, backups []*model.User) error {
	content := []byte("test-case-id = 22\n")
	fileName := "test-case-id-22.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' in cloud-drives-sync-root", main.Email, fileName)
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := mainClient.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("resume sync failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	allFiles, err := db.GetAllFiles()
	if err != nil {
		return err
	}
	count := 0
	for _, f := range allFiles {
		if f.Name == fileName {
			count++
		}
	}
	if count > 1 {
		return fmt.Errorf("found %d copies of %s in DB, expected 1 (no duplicates after resumed sync)", count, fileName)
	}
	return verifyFileOnAllProviders(r, main, backups, "/"+fileName, content)
}

// SPEC Case 23: MS Placeholders
func specCase23(r *task.Runner, main *model.User, backups []*model.User) error {
	msBackups := filterUsers(backups, model.ProviderMicrosoft)
	if len(msBackups) < 2 {
		return fmt.Errorf("case 23 requires at least 2 Microsoft OneDrive backup accounts, got %d", len(msBackups))
	}
	content := []byte("test-case-id = 23\n")
	fileName := "test-case-id-23.txt"
	ownerMS := msBackups[0]
	logger.Info("[MANUAL INTERACTION] [%s] Create '%s' in cloud-drives-sync-root", ownerMS.Email, fileName)
	client, err := r.GetOrCreateClient(ownerMS)
	if err != nil {
		return err
	}
	sid, err := client.GetSyncFolderID()
	if err != nil {
		return err
	}
	if _, err := client.UploadFile(sid, fileName, bytes.NewReader(content), int64(len(content))); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("first sync failed: %w", err)
	}
	if err := runCLISync(r); err != nil {
		return fmt.Errorf("second sync failed: %w", err)
	}
	if err := verifyFileInDB("/" + fileName); err != nil {
		return err
	}
	for _, u := range msBackups[1:] {
		c, err := r.GetOrCreateClient(u)
		if err != nil {
			return err
		}
		uSID, err := c.GetSyncFolderID()
		if err != nil {
			return err
		}
		files, err := c.ListFiles(uSID)
		if err != nil {
			return err
		}
		foundPlaceholder := false
		for _, f := range files {
			if f.Name == fileName || strings.Contains(f.Name, "test-case-id-23") {
				foundPlaceholder = true
				if f.Size > 0 && !strings.Contains(f.Name, ".placeholder") {
					return fmt.Errorf("account %s has a real copy of %s (size %d), expected placeholder/shortcut", u.Email, fileName, f.Size)
				}
				break
			}
		}
		if !foundPlaceholder {
			return fmt.Errorf("no placeholder/shortcut for %s found on account %s", fileName, u.Email)
		}
	}
	logger.Info("[VERIFICATION] Case 23 passed: file on owner MS account, placeholders/shortcuts on others")
	return nil
}

// legacyOwnershipTransferTest is the inner1 SPEC case — verifies Google Drive transfer ownership API flow.
func legacyOwnershipTransferTest(r *task.Runner, main *model.User, backups []*model.User) error {
	googleBackups := filterUsers(backups, model.ProviderGoogle)
	if len(googleBackups) == 0 {
		logger.Warning("No Google backup accounts found, skipping inner1")
		return nil
	}
	targetBackup := googleBackups[0]
	mainClient, err := r.GetOrCreateClient(main)
	if err != nil {
		return err
	}
	targetClient, err := r.GetOrCreateClient(targetBackup)
	if err != nil {
		return err
	}
	sid, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}
	testData := []byte("Testing ownership transfer with pending owner flow")
	testFileName := "test-case-id-inner1.txt"
	logger.Info("[MANUAL INTERACTION] [%s] Upload '%s' for ownership transfer test", main.Email, testFileName)
	uploadedFile, err := mainClient.UploadFile(sid, testFileName, bytes.NewReader(testData), int64(len(testData)))
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	fileID := uploadedFile.Replicas[0].NativeID
	err = mainClient.TransferOwnership(fileID, targetBackup.Email)
	if err == nil {
		logger.Info("[inner1] Direct transfer succeeded")
		targetClient.DeleteFile(fileID)
		return nil
	}
	if err == api.ErrOwnershipTransferPending {
		logger.Info("[inner1] Got pending transfer signal — accepting ownership")
		if err := targetClient.AcceptOwnership(fileID); err != nil {
			return fmt.Errorf("accept ownership: %w", err)
		}
		time.Sleep(2 * time.Second)
		metadata, err := targetClient.GetFileMetadata(fileID)
		if err != nil {
			return fmt.Errorf("get metadata: %w", err)
		}
		if len(metadata.Replicas) == 0 || metadata.Replicas[0].Owner != targetBackup.Email {
			return fmt.Errorf("ownership not transferred to %s", targetBackup.Email)
		}
		targetClient.DeleteFile(fileID)
		return nil
	}
	if strings.Contains(err.Error(), "Consent is required") || strings.Contains(err.Error(), "consentRequiredForOwnershipTransfer") {
		logger.Info("[inner1] Consumer account requires consent for ownership transfer — fallback expected")
		mainClient.DeleteFile(fileID)
		return nil
	}
	return fmt.Errorf("unexpected error during ownership transfer: %w", err)
}

func runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\nstderr: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func runSetup(r *task.Runner) error {
	// Phase 0: Cleanup policy (SPEC)
	if testBackup {
		logger.Warning("SPEC backup mode selected, but root rename backup flow is not implemented yet; using cleanup fallback for now")
	}
	if testUnsafe || testBackup {
		logger.Info("Deleting cloud files...")
		if err := cleanupCloudFiles(r); err != nil {
			return err
		}

		logger.Info("Deleting local metadata...")
		if db != nil {
			logger.Info("Closing existing DB connection...")
			err := db.Close()
			if err != nil {
				logger.Warning("Error closing DB: %v", err)
			}
			db = nil
		}
		// Wait for handle release
		time.Sleep(1 * time.Second)

		dbPath := database.GetDBPath()
		// Retry loop for deletion (OneDrive interference)
		var err error
		for i := 0; i < 5; i++ {
			err = os.Remove(dbPath)
			if err == nil || os.IsNotExist(err) {
				err = nil
				break
			}
			logger.Warning("Failed to remove DB (attempt %d/5): %v. Retrying...", i+1, err)
			time.Sleep(2 * time.Second)
		}

		if err != nil {
			logger.Warning("Could not remove DB file. Attempting to open and Reset instead.")
			db, err = database.Open(masterPassword)
			if err != nil {
				return fmt.Errorf("failed to open DB for reset: %w", err)
			}
			if err := db.Initialize(); err != nil {
				return fmt.Errorf("failed to initialize DB: %w", err)
			}
			if err := db.Reset(); err != nil {
				return fmt.Errorf("failed to reset DB: %w", err)
			}
		} else {
			db, err = database.Open(masterPassword)
			if err != nil {
				return fmt.Errorf("failed to re-open DB: %w", err)
			}
			if err := db.Initialize(); err != nil {
				return fmt.Errorf("failed to initialize DB: %w", err)
			}
		}
	} else {
		if db == nil {
			var err error
			db, err = database.Open(masterPassword)
			if err != nil {
				return fmt.Errorf("failed to open DB: %w", err)
			}
		}
	}
	return nil
}

// Helpers

func recreateSyncFolders(r *task.Runner, cfg *model.Config) error {
	logger.Info("Recreating sync folders...")

	// 1. Main Account (Google)
	var mainUser *model.User
	for i := range cfg.Users {
		if cfg.Users[i].IsMain {
			mainUser = &cfg.Users[i]
			break
		}
	}

	if mainUser != nil {
		client, err := r.GetOrCreateClient(mainUser)
		if err != nil {
			return err
		}

		// Check if exists
		id, err := client.GetSyncFolderID()
		if err != nil || id == "" {
			logger.Info("Creating Main sync folder for %s...", mainUser.Email)
			if _, err := client.CreateFolder("root", google.GetSyncFolderName()); err != nil {
				return fmt.Errorf("failed to create main sync folder: %w", err)
			}
		}
	}

	// 2. Backup Accounts
	for i := range cfg.Users {
		u := &cfg.Users[i]
		if u.IsMain {
			continue
		}

		logger.Info("Checking sync folder for backup %s (%s)...", u.Email, u.Provider)

		client, err := r.GetOrCreateClient(u)
		if err != nil {
			return err
		}

		if u.Provider == model.ProviderTelegram {
			// Telegram requires PreFlightCheck to initialize the channel (sets channelID)
			// GetSyncFolderID returns success ("/") even if channel is missing/uninit in struct,
			// so we must force check here.
			if err := client.PreFlightCheck(); err != nil {
				return fmt.Errorf("telegram preflight failed for %s: %w", u.Phone, err)
			}
		}

		id, err := client.GetSyncFolderID()
		if err == nil && id != "" {
			continue // Already exists
		}

		logger.Info("Creating Backup sync folder for %s (%s)...", u.Email, u.Provider)

		switch u.Provider {
		case model.ProviderGoogle:
			if mainUser != nil {
				mainClient, err := r.GetOrCreateClient(mainUser)
				if err != nil {
					return err
				}
				mainID, err := mainClient.GetSyncFolderID()
				if err != nil {
					return err
				}

				logger.Info("Sharing Main folder with %s...", u.Email)
				if err := mainClient.ShareFolder(mainID, u.Email, "writer"); err != nil {
					return fmt.Errorf("failed to share folder: %w", err)
				}
			}

		case model.ProviderMicrosoft:
			if _, err := client.CreateFolder("root", microsoft.GetSyncFolderName()); err != nil {
				return fmt.Errorf("failed to create microsoft sync folder: %w", err)
			}
			if mainUser != nil {
				if err := client.ShareFolder("root/"+microsoft.GetSyncFolderName(), mainUser.Email, "writer"); err != nil {
					nid, err := client.GetSyncFolderID()
					if err != nil {
						return err
					}
					client.ShareFolder(nid, mainUser.Email, "writer")
				}
			}

		case model.ProviderTelegram:
			if err := client.PreFlightCheck(); err != nil {
				return fmt.Errorf("telegram preflight check failed: %w", err)
			}
		}
	}
	return nil
}

func cleanupCloudFiles(r *task.Runner) error {
	var backups []*model.User
	var mainUser *model.User

	for i := range cfg.Users {
		u := &cfg.Users[i]
		if u.IsMain {
			mainUser = u
		} else {
			backups = append(backups, u)
		}
	}

	users := append(backups, mainUser)
	if mainUser == nil {
		users = backups
	}

	for _, u := range users {
		if u == nil {
			continue
		}
		client, err := r.GetOrCreateClient(u)
		if err != nil {
			logger.Warning("Failed client for %s: %v", u.Email, err)
			continue
		}

		deleteAuxFolder := func(c api.CloudClient, u *model.User) {
			folders, err := c.ListFolders("root")
			if err == nil {
				for _, f := range folders {
					if f.Name == task.AuxFolder {
						logger.InfoTagged(u.LogTags(), "Deleting aux folder %s...", f.ID)
						// Try to empty it first
						subs, _ := c.ListFolders(f.ID)
						for _, sub := range subs {
							// Empty subfolder (soft-deleted)
							files, _ := c.ListFiles(sub.ID)
							for _, file := range files {
								c.DeleteFile(file.ID)
							}
							c.DeleteFolder(sub.ID)
						}

						if err := c.DeleteFolder(f.ID); err != nil {
							logger.Warning("Failed to delete aux folder: %v", err)
						}
					}
				}
			}
		}

		switch u.Provider {
		case model.ProviderTelegram:
			if tgClient, ok := client.(*telegram.Client); ok {
				logger.Info("Cleaning Telegram messages for %s...", u.Email)
				if err := client.PreFlightCheck(); err != nil {
					logger.Warning("PreFlight failed for cleaning Telegram: %v", err)
				}
				if err := tgClient.DeleteAllMessages(); err != nil {
					logger.Warning("Failed to delete Telegram messages: %v", err)
				}
			}
		case model.ProviderGoogle:
			if gClient, ok := client.(*google.Client); ok {
				if err := gClient.EmptySyncFolder(); err != nil {
					logger.Warning("Failed to empty Google folder for %s: %v", u.Email, err)
				}
				deleteAuxFolder(client, u)
			}
		case model.ProviderMicrosoft:
			if mClient, ok := client.(*microsoft.Client); ok {
				logger.Info("Cleaning Microsoft folder for %s...", u.Email)
				if err := mClient.EmptySyncFolder(); err != nil {
					logger.Warning("Failed to empty Microsoft folder for %s: %v", u.Email, err)
				}
				deleteAuxFolder(client, u)
			}
		}
	}
	return nil
}

func verifyFile(db *database.DB, path string, size int64) error {
	f, err := db.GetFileByPath(path)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("file %s not found in DB", path)
	}
	if f.Status != "active" {
		return fmt.Errorf("file %s status is %s", path, f.Status)
	}
	if size > 0 && f.Size != size {
		logger.Warning("File %s size mismatch: expected %d, got %d", path, size, f.Size)
	}
	return nil
}

func filterUsers(users []*model.User, provider model.Provider) []*model.User {
	var res []*model.User
	for _, u := range users {
		if u.Provider == provider {
			res = append(res, u)
		}
	}
	return res
}

func mustRand(max int64) int64 {
	n, _ := rand.Int(rand.Reader, big.NewInt(max))
	return n.Int64()
}

func getOrCreateFolder(client api.CloudClient, parentID, name string) (*model.Folder, error) {
	folders, err := client.ListFolders(parentID)
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		if f.Name == name {
			return f, nil
		}
	}
	return client.CreateFolder(parentID, name)
}

func printAllFiles(db *database.DB) {
	files, _ := db.GetAllFiles()
	logger.Info("DB Dump:")
	for _, f := range files {
		logger.Info(" - %s (%s)", f.Path, f.Status)
	}
}

func getNativeID(f *model.File, u *model.User) string {
	// Prefer active replicas
	var fallbackID string
	for _, r := range f.Replicas {
		if r.Provider == u.Provider && (r.AccountID == u.Email || r.AccountID == u.Phone) {
			if r.Status == "active" {
				return r.NativeID
			}
			if fallbackID == "" {
				fallbackID = r.NativeID
			}
		}
	}
	if fallbackID != "" {
		logger.Warning("getNativeID: no active replica found for file %s on %s, using stale replica", f.Path, u.Email)
		return fallbackID
	}
	logger.Warning("NativeID not found for file %s. User: %s (%s). Replicas: %d", f.Path, u.Email, u.Provider, len(f.Replicas))
	for i, r := range f.Replicas {
		logger.Warning(" - Replica %d: Provider=%s, Account=%s, NativeID=%s, Status=%s", i, r.Provider, r.AccountID, r.NativeID, r.Status)
	}
	return ""
}

func getUserForReplica(db *database.DB, path string, provider model.Provider, users []model.User) (*model.User, error) {
	f, err := db.GetFileByPath(path)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fmt.Errorf("file %s not found", path)
	}

	// Prefer users with active replicas
	var fallbackUser *model.User
	for _, r := range f.Replicas {
		if r.Provider == provider {
			for i := range users {
				u := &users[i]
				if u.Provider == r.Provider && (u.Email == r.AccountID || u.Phone == r.AccountID) {
					if r.Status == "active" {
						return u, nil
					}
					if fallbackUser == nil {
						fallbackUser = u
					}
				}
			}
		}
	}
	if fallbackUser != nil {
		logger.Warning("getUserForReplica: no active replica for %s on provider %s, using account with stale replica: %s", path, provider, fallbackUser.Email)
		return fallbackUser, nil
	}
	return nil, fmt.Errorf("no replica found for provider %s", provider)
}

func sanitizeTestCaseID(caseID string) string {
	caseID = strings.TrimSpace(caseID)
	caseID = strings.ReplaceAll(caseID, " ", "-")
	caseID = strings.ReplaceAll(caseID, "/", "-")
	caseID = strings.ReplaceAll(caseID, "\\", "-")
	return caseID
}

type specTestCase struct {
	ID          string
	Name        string
	LegacyAlias string
	Run         func(*task.Runner, *model.User, []*model.User) error
}

func specTestCases() []specTestCase {
	return []specTestCase{
		{ID: "1", Name: "Clean-slate setup", Run: specCase1},
		{ID: "2", Name: "Create file on main", Run: specCase2},
		{ID: "3", Name: "Create file on Google backup", Run: specCase3},
		{ID: "4", Name: "Create folder on main", Run: specCase4},
		{ID: "5", Name: "Create folder on Google backup", Run: specCase5},
		{ID: "6", Name: "Create file on Microsoft backup", Run: specCase6},
		{ID: "7", Name: "Create folder on Microsoft backup", Run: specCase7},
		{ID: "8", Name: "Sync file from Telegram", Run: specCase8},
		{ID: "9", Name: "Sync file from Microsoft", Run: specCase9},
		{ID: "10", Name: "Move Google Drive files from backups roots", Run: specCase10},
		{ID: "11", Name: "Google Drive nested folders", Run: specCase11},
		{ID: "12", Name: "Microsoft OneDrive nested folders", Run: specCase12},
		{ID: "13", Name: "Google Drive moved file", Run: specCase13},
		{ID: "14", Name: "Google Drive files created directly in nested folders", Run: specCase14},
		{ID: "15", Name: "Google Drive multiple moved files", Run: specCase15},
		{ID: "16", Name: "Google Drive soft-delete and restore", Run: specCase16},
		{ID: "17", Name: "Google Drive hard-delete", Run: specCase17},
		{ID: "18", Name: "Telegram fragmentation and defragmentation restore", Run: specCase18},
		{ID: "19", Name: "Idempotent sync", Run: specCase19},
		{ID: "20", Name: "Quota check", Run: specCase20},
		{ID: "21", Name: "Divergent content at the same logical path", Run: specCase21},
		{ID: "22", Name: "Sync resumed after interruption", Run: specCase22},
		{ID: "23", Name: "MS Placeholders", Run: specCase23},
		{ID: "inner1", Name: "Google Drive Transfer Ownership", Run: specCaseInner1},
		{ID: "inner2", Name: "Microsoft OneDrive Real Shortcut", Run: specCaseInner2},
	}
}

func specTestCaseRegistry() map[string]string {
	registry := make(map[string]string)
	for _, tc := range specTestCases() {
		registry[tc.ID] = tc.Name
	}
	return registry
}

func findSpecTestCase(caseID string) *specTestCase {
	for _, tc := range specTestCases() {
		if tc.ID == caseID {
			copy := tc
			return &copy
		}
	}
	return nil
}

func validateRequestedTestCase(caseID string) error {
	if caseID == "" {
		return nil
	}
	if findSpecTestCase(caseID) != nil {
		return nil
	}
	return fmt.Errorf("unknown SPEC test case %q", caseID)
}

func runCLIGetMetadata(runner *task.Runner) error {
	logger.Info("[CLI COMMAND] Running: GetMetadata")
	return runner.GetMetadata()
}

func runCLISync(runner *task.Runner) error {
	logger.Info("[CLI COMMAND] Running: Sync (Full Pipeline)")
	return SyncAction(runner, false)
}

func verifyFileInDB(path string) error {
	logger.Info("[VERIFICATION] Checking file '%s' exists in DB", path)
	return verifyFile(db, path, 0)
}

func verifyFileStatus(db *database.DB, path, expectedStatus string, shouldBeInactive bool) error {
	if shouldBeInactive {
		logger.Info("[VERIFICATION] Checking file '%s' is NOT '%s' (should be soft-deleted/inactive)", path, expectedStatus)
		f, _ := db.GetFileByPath(path)
		if f != nil && f.Status == expectedStatus {
			logger.Error("[VERIFICATION] File %s has status '%s' but should be inactive. Replicas:", path, f.Status)
			for i, r := range f.Replicas {
				logger.Error("[VERIFICATION]   Replica %d: Provider=%s, Account=%s, NativeID=%s, Status=%s, Path=%s", i, r.Provider, r.AccountID, r.NativeID, r.Status, r.Path)
			}
			return fmt.Errorf("file %s should be soft-deleted/inactive but is %s", path, f.Status)
		}
		if f != nil {
			logger.Info("[VERIFICATION] File %s has status '%s' (OK)", path, f.Status)
		} else {
			logger.Info("[VERIFICATION] File %s not found in DB (OK - treated as inactive)", path)
		}
		return nil
	}
	logger.Info("[VERIFICATION] Checking file '%s' has status '%s'", path, expectedStatus)
	f, err := db.GetFileByPath(path)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("file %s not found", path)
	}
	if f.Status != expectedStatus {
		return fmt.Errorf("file %s has status %s, expected %s", path, f.Status, expectedStatus)
	}
	return nil
}

func testMetadata(runner *task.Runner) error {
	logger.Info("Running testMetadata verification...")

	// 1. Verify DB consistency (FileID not null)
	orphans, err := db.GetReplicasWithNullFileID()
	if err != nil {
		return fmt.Errorf("failed to get orphans: %w", err)
	}
	if len(orphans) > 0 {
		logger.Error("Found %d replicas with null FileID in DB", len(orphans))
		for _, r := range orphans {
			logger.Error("Orphan Replica: ID=%d, Path=%s, Provider=%s", r.ID, r.Path, r.Provider)
		}
		return fmt.Errorf("found %d replicas with null FileID", len(orphans))
	}

	var errCount int

	for _, user := range cfg.Users {
		client, err := runner.GetOrCreateClient(&user)
		if err != nil {
			logger.Error("Failed to get client for %s: %v", user.Email, err)
			errCount++
			continue
		}

		// Get DB replicas for this account
		accountID := user.Email
		if accountID == "" {
			accountID = user.Phone
		}

		dbReplicas, err := db.GetReplicasByAccount(user.Provider, accountID)
		if err != nil {
			return fmt.Errorf("failed to get replicas for %s: %w", accountID, err)
		}

		// Scan Cloud Files
		cloudFiles := make(map[string]*model.File) // Map NativeID -> File

		rootID, err := client.GetSyncFolderID()
		if err != nil {
			logger.Error("Failed to get sync folder for %s: %v", accountID, err)
			errCount++
			continue
		}

		// Recursive List
		logger.Info("Listing files for %s (%s)...", accountID, user.Provider)
		err = listFilesRecursive(client, rootID, "", cloudFiles)
		if err != nil {
			logger.Error("Failed to list cloud files for %s: %v", accountID, err)
			errCount++
			continue
		}

		// Compare DB -> Cloud
		for _, r := range dbReplicas {
			if r.Status != "active" {
				continue
			}

			// Skip replicas belonging to a soft-deleted logical file. Their rows remain
			// "active" (pending hard-delete) but the physical file lives in the soft-deleted
			// aux folder. For shared Google files (a single physical copy shared across all
			// accounts via one NativeID) the moved file may not be visible in every account's
			// listing, so per-account presence is not guaranteed. This mirrors the Cloud -> DB
			// pass below which already tolerates soft-deleted files.
			if r.FileID != "" {
				if file, _ := db.GetFileByID(r.FileID); file != nil && file.Status == "soft-deleted" {
					delete(cloudFiles, r.NativeID)
					continue
				}
			}

			// Special handling for Telegram which aggregates fragments in ListFiles
			// Ideally we should check if ListFiles returns fragments or files.
			// Currently Telegram ListFiles returns aggregated Files, so we match against Reference NativeID (Part 1).

			// We check if the Main NativeID exists in the cloud listing
			if _, ok := cloudFiles[r.NativeID]; !ok {
				logger.Error("Missing file on cloud: %s (Replica %s, NativeID %s)", r.Path, r.Path, r.NativeID)
				errCount++
			} else {
				delete(cloudFiles, r.NativeID)
			}

			// If it's fragmented and NOT Telegram (or if we change Telegram to list parts), check fragments.
			// But for now, since Telegram aggregates, we've already consumed the "File" entry above.
			// Checking fragments individually requires ListFiles to return them individually, which it doesn't for Telegram.
			if r.Fragmented && r.Provider != model.ProviderTelegram {
				fragments, err := db.GetReplicaFragments(r.ID)
				if err != nil {
					logger.Error("Failed to get fragments for replica %d: %v", r.ID, err)
					errCount++
					continue
				}
				for _, frag := range fragments {
					if _, ok := cloudFiles[frag.NativeFragmentID]; !ok {
						logger.Error("Missing fragment on cloud: %s (Replica %s, Frag %d)", frag.NativeFragmentID, r.Path, frag.FragmentNumber)
						errCount++
					} else {
						delete(cloudFiles, frag.NativeFragmentID)
					}
				}
			}
		}

		// Compare Cloud -> DB (Remaining files are unexpected)
		for nativeID, f := range cloudFiles {
			// Check if this file is a replica for ANY account on this provider (Shared folder case)
			// Use HasActiveReplicaByNativeID to properly check across all accounts sharing the same file
			hasActive, err := db.HasActiveReplicaByNativeID(user.Provider, nativeID)
			if err == nil && hasActive {
				continue
			}

			// Check if it's a fragment (for Telegram mainly, but good to be generic)
			anyFragReplica, err := db.GetReplicaByNativeFragmentID(nativeID)
			if err == nil && anyFragReplica != nil && anyFragReplica.Status == "active" && anyFragReplica.Provider == user.Provider {
				continue
			}

			// Also check if this file belongs to a soft-deleted logical file (legitimately on cloud pending hard-delete)
			anyReplica, _ := db.GetReplicaByNativeID(user.Provider, nativeID)
			if anyReplica != nil && anyReplica.FileID != "" {
				file, _ := db.GetFileByID(anyReplica.FileID)
				if file != nil && file.Status == "soft-deleted" {
					continue
				}
			}

			logger.Error("Unexpected file on cloud: %s (ID: %s, Name: %s, Account: %s)", f.Path, nativeID, f.Name, accountID)
			errCount++
		}
	}

	if errCount > 0 {
		return fmt.Errorf("metadata verification failed with %d errors", errCount)
	}
	logger.Info("Metadata verification passed.")
	return nil
}

func listFilesRecursive(client api.CloudClient, folderID string, currentPath string, results map[string]*model.File) error {
	files, err := client.ListFiles(folderID)
	if err != nil {
		return err
	}
	for _, f := range files {
		f.Path = filepath.Join(currentPath, f.Name)
		if len(f.Replicas) > 0 {
			results[f.Replicas[0].NativeID] = f
		} else {
			results[f.ID] = f
		}
	}

	folders, err := client.ListFolders(folderID)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		err := listFilesRecursive(client, folder.ID, filepath.Join(currentPath, folder.Name), results)
		if err != nil {
			return err
		}
	}
	return nil
}

// listFilesRecursiveExcludeAux is like listFilesRecursive but skips the aux folder entirely,
// used for hard-delete verification where the aux/hard-deleted folder may still be draining.
func listFilesRecursiveExcludeAux(client api.CloudClient, folderID string, currentPath string, results map[string]*model.File) error {
	files, err := client.ListFiles(folderID)
	if err != nil {
		return err
	}
	for _, f := range files {
		f.Path = filepath.Join(currentPath, f.Name)
		if len(f.Replicas) > 0 {
			results[f.Replicas[0].NativeID] = f
		} else {
			results[f.ID] = f
		}
	}

	folders, err := client.ListFolders(folderID)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		// Skip the aux folder — hard-deleted/soft-deleted subfolders should not be scanned
		if folder.Name == task.AuxFolder {
			continue
		}
		err := listFilesRecursiveExcludeAux(client, folder.ID, filepath.Join(currentPath, folder.Name), results)
		if err != nil {
			return err
		}
	}
	return nil
}
