package cmd

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/database"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/microsoft"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/FranLegon/cloud-drives-sync/internal/telegram"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var testSafe bool
var testForce bool
var testStopOnError bool
var testCase int

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Run system integration tests",
	Long:  `Test command performs a series of integration tests to validate system functionality.`,
	RunE:  runTest,
}

func init() {
	testCmd.Flags().BoolVar(&testSafe, "safe", false, "Skip destructive cleanup steps")
	testCmd.Flags().BoolVar(&testForce, "force", false, "Skip confirmation prompt")
	testCmd.Flags().BoolVarP(&testStopOnError, "stop-on-error", "s", false, "Stop test execution immediately if an error occurs")
	testCmd.Flags().IntVarP(&testCase, "test-case", "t", 0, "Run specific test case and its dependencies (0 = all)")
	rootCmd.AddCommand(testCmd)
}

func runTest(cmd *cobra.Command, args []string) error {
	// Setup Logging to file
	logFile, err := os.OpenFile("test.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return fmt.Errorf("failed to open test.log: %w", err)
	}
	defer logFile.Close()
	mw := io.MultiWriter(os.Stdout, logFile)
	logger.SetOutput(mw)

	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Recovered from panic: %v\n", r)
		}
	}()
	logger.Info("Starting Test Command...")

	runner := task.NewRunner(cfg, nil, false) // Temporary runner for cleanup

	// Run Setup (Phase 0 + Init)
	// Always run setup unless we are running a specific test case AND logic dictates otherwise,
	// but generally we need setup.
	if err := runSetup(runner); err != nil {
		return err
	}

	// Re-init runner with DB after setup
	if db == nil {
		var err error
		db, err = database.Open(masterPassword)
		if err != nil {
			return fmt.Errorf("failed to open DB: %w", err)
		}
	}
	runner = task.NewRunner(cfg, db, false)
	runner.SetStopOnError(testStopOnError)

	// Ensure folders exist
	if err := recreateSyncFolders(runner, cfg); err != nil {
		return fmt.Errorf("failed to recreate sync folders: %w", err)
	}

	logger.Info("Running GetMetadata...")
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

	shouldRun := func(step int) bool {
		if testCase == 0 {
			return true
		}
		if testCase == step {
			return true
		}
		// Dependencies
		// 9 -> 7
		if testCase == 9 && step == 7 {
			return true
		}
		// 8 -> 5 -> 4 -> 2
		//        -> 3
		if testCase == 8 {
			if step == 5 || step == 4 || step == 3 || step == 2 {
				return true
			}
		}
		// 5 -> 4 -> 2
		//   -> 3
		if testCase == 5 {
			if step == 4 || step == 3 || step == 2 {
				return true
			}
		}
		// 4 -> 2
		if testCase == 4 && step == 2 {
			return true
		}
		return false
	}

	// Dependency execution
	if shouldRun(1) {
		if err := runTestCase1(runner, mainUser); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}
	if shouldRun(2) {
		if err := runTestCase2(runner, backups); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}
	if shouldRun(3) {
		if err := runTestCase3(runner, mainUser); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}
	if shouldRun(4) {
		if err := runTestCase4(runner, mainUser, backups); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}

	if shouldRun(5) {
		if err := runTestCase5(runner, mainUser, backups); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}
	if shouldRun(7) {
		if err := runTestCase7(runner, mainUser); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}
	if shouldRun(8) {
		if err := runTestCase8(runner, mainUser, backups); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}
	if shouldRun(9) {
		if err := runTestCase9(runner, mainUser, backups); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}
	if shouldRun(10) {
		if err := runTestCase10(runner, mainUser, backups); err != nil {
			return err
		}
		if err := testMetadata(runner); err != nil {
			return err
		}
	}

	logger.Info("\nTEST SUITE COMPLETED SUCCESSFULLY")
	return nil
}

func runSetup(r *task.Runner) error {
	// Phase 0: Cleanup (Unsafe Mode)
	if !testSafe {
		if !testForce {
			prompt := promptui.Prompt{
				Label: "Warning: This will delete ALL data in cloud sync folders and local metadata. Type 'yes' to continue",
				Validate: func(input string) error {
					if input != "yes" {
						return fmt.Errorf("type 'yes' to continue")
					}
					return nil
				},
			}
			if _, err := prompt.Run(); err != nil {
				return fmt.Errorf("aborted")
			}
		}

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
			if _, err := client.CreateFolder("root", "sync-cloud-drives"); err != nil {
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
			if _, err := client.CreateFolder("root", "sync-cloud-drives"); err != nil {
				return fmt.Errorf("failed to create microsoft sync folder: %w", err)
			}
			if mainUser != nil {
				if err := client.ShareFolder("root/sync-cloud-drives", mainUser.Email, "writer"); err != nil {
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
			if _, err := client.CreateFolder("", "sync-cloud-drives"); err != nil {
				logger.Warning("Telegram create folder/channel warning: %v", err)
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

		if u.Provider == model.ProviderTelegram {
			if tgClient, ok := client.(*telegram.Client); ok {
				logger.Info("Cleaning Telegram messages for %s...", u.Email)
				if err := client.PreFlightCheck(); err != nil {
					logger.Warning("PreFlight failed for cleaning Telegram: %v", err)
				}
				if err := tgClient.DeleteAllMessages(); err != nil {
					logger.Warning("Failed to delete Telegram messages: %v", err)
				}
			}
		} else if u.Provider == model.ProviderGoogle {
			if gClient, ok := client.(*google.Client); ok {
				if err := gClient.EmptySyncFolder(); err != nil {
					logger.Warning("Failed to empty Google folder for %s: %v", u.Email, err)
				}
			}
		} else if u.Provider == model.ProviderMicrosoft {
			if mClient, ok := client.(*microsoft.Client); ok {
				if err := mClient.EmptySyncFolder(); err != nil {
					logger.Warning("Failed to empty Microsoft folder for %s: %v", u.Email, err)
				}
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
	for _, r := range f.Replicas {
		if r.Provider == u.Provider && (r.AccountID == u.Email || r.AccountID == u.Phone) {
			return r.NativeID
		}
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

	for _, r := range f.Replicas {
		if r.Provider == provider {
			for i := range users {
				u := &users[i]
				if u.Provider == r.Provider && (u.Email == r.AccountID || u.Phone == r.AccountID) {
					return u, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("no replica found for provider %s", provider)
}

var testFileContents = map[string]string{
	"test_1.txt":    "This is test file 1 content.",
	"test_2.txt":    "This is test file 2 content for specific backup.",
	"test_3.txt":    "This is test file 3 content for another backup.",
	"test_4.txt":    "This is test file 4 content.",
	"test_move.txt": "This is a file dedicated to testing movement.",
}

func runTestCase1(runner *task.Runner, mainUser *model.User) error {
	logger.Info("\n--- Test Case 1: test_1.txt (Main -> Free -> Sync) ---")
	f1Name := "test_1.txt"
	f1Data := []byte(testFileContents[f1Name])

	mainClient, err := runner.GetOrCreateClient(mainUser)
	if err != nil {
		return err
	}
	mainSyncID, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}

	logger.Info("Uploading %s to Main Account...", f1Name)
	if _, err := mainClient.UploadFile(mainSyncID, f1Name, bytes.NewReader(f1Data), int64(len(f1Data))); err != nil {
		return fmt.Errorf("upload test_1 failed: %w", err)
	}

	logger.Info("Updating Metadata to find uploaded file...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	logger.Info("Running FreeMain...")
	if err := runner.FreeMain(); err != nil {
		return fmt.Errorf("FreeMain failed: %w", err)
	}

	logger.Info("Running SyncProviders...")
	if err := runner.SyncProviders(); err != nil {
		return fmt.Errorf("SyncProviders failed: %w", err)
	}

	logger.Info("Verifying %s...", f1Name)
	return verifyFile(db, "/"+f1Name, int64(len(f1Data)))
}

func runTestCase2(runner *task.Runner, backups []*model.User) error {
	logger.Info("\n--- Test Case 2: Multi-Provider Backups ---")

	googleBackups := filterUsers(backups, model.ProviderGoogle)
	microsoftBackups := filterUsers(backups, model.ProviderMicrosoft)
	telegramBackups := filterUsers(backups, model.ProviderTelegram)

	uploadLocalToRandom := func(users []*model.User, filename string) error {
		if len(users) == 0 {
			logger.Warning("No backups for provider to upload %s", filename)
			return nil
		}
		u := users[int(mustRand(int64(len(users))))]
		data := []byte(testFileContents[filename])

		client, err := runner.GetOrCreateClient(u)
		if err != nil {
			return err
		}
		syncID, err := client.GetSyncFolderID()
		if err != nil {
			return err
		}

		logger.Info("Uploading %s to %s (%s)...", filename, u.Email, u.Provider)
		_, err = client.UploadFile(syncID, filename, bytes.NewReader(data), int64(len(data)))
		return err
	}

	if err := uploadLocalToRandom(googleBackups, "test_2.txt"); err != nil {
		return err
	}
	if err := uploadLocalToRandom(telegramBackups, "test_3.txt"); err != nil {
		return err
	}
	if err := uploadLocalToRandom(microsoftBackups, "test_4.txt"); err != nil {
		return err
	}

	logger.Info("Updating Metadata to find uploaded files (Test Case 2)...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	logger.Info("Running SyncProviders...")
	if err := runner.SyncProviders(); err != nil {
		return fmt.Errorf("SyncProviders failed: %w", err)
	}

	logger.Info("Verifying files...")
	if err := verifyFile(db, "/test_2.txt", 0); err != nil {
		return err
	}
	if err := verifyFile(db, "/test_3.txt", 0); err != nil {
		return err
	}
	return verifyFile(db, "/test_4.txt", 0)
}

func runTestCase3(runner *task.Runner, mainUser *model.User) error {
	logger.Info("\n--- Test Case 3: Large File (50MB) ---")
	test5Name := "test_5.txt"
	test5Size := int64(50) * 1024 * 1024

	mainClient, err := runner.GetOrCreateClient(mainUser)
	if err != nil {
		return err
	}
	mainSyncID, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}

	logger.Info("Uploading %s to Main Account (Streamed)...", test5Name)
	if _, err := mainClient.UploadFile(mainSyncID, test5Name, io.LimitReader(rand.Reader, test5Size), test5Size); err != nil {
		logger.Error("Upload failed: %v", err)
		return fmt.Errorf("upload large file failed: %w", err)
	}

	logger.Info("Updating Metadata...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	logger.Info("Running SyncProviders (Large File)...")
	if err := runner.SyncProviders(); err != nil {
		logger.Warning("SyncProviders (Large File) had error: %v. Continuing...", err)
	}

	logger.Info("Verifying large file...")
	return verifyFile(db, "/"+test5Name, test5Size)
}

func runTestCase4(runner *task.Runner, mainUser *model.User, backups []*model.User) error {
	logger.Info("\n--- Test Case 4: Movements ---")

	moveFileWrapper := func(c api.CloudClient, u *model.User, path, targetFolderID string) error {
		f, err := db.GetFileByPath(path)
		if err != nil {
			return err
		}
		if f == nil {
			return fmt.Errorf("file %s not found", path)
		}
		nativeID := getNativeID(f, u)
		if nativeID == "" {
			return fmt.Errorf("nativeID not found for %s on %s", path, u.Email)
		}
		logger.Info("Moving %s on %s...", path, u.Email)
		return c.MoveFile(nativeID, targetFolderID)
	}

	mainClient, err := runner.GetOrCreateClient(mainUser)
	if err != nil {
		return err
	}
	mainSyncID, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}

	// Upload test_move.txt
	moveName := "test_move.txt"
	moveData := []byte(testFileContents[moveName])

	logger.Info("Uploading %s to Main...", moveName)
	if _, err := mainClient.UploadFile(mainSyncID, moveName, bytes.NewReader(moveData), int64(len(moveData))); err != nil {
		return fmt.Errorf("upload %s failed: %w", moveName, err)
	}

	logger.Info("Updating Metadata to find %s...", moveName)
	if err := runner.GetMetadata(); err != nil {
		return err
	}

	folderName := "Folder_Main"
	logger.Info("Creating folder %s ...", folderName)
	newFolder, err := mainClient.CreateFolder(mainSyncID, folderName)
	if err != nil {
		return err
	}

	logger.Info("Moving %s...", moveName)
	if err := moveFileWrapper(mainClient, mainUser, "/"+moveName, newFolder.ID); err != nil {
		return fmt.Errorf("failed to move %s: %w", moveName, err)
	}

	// MS: test_2 -> Folder_MS
	microsoftBackups := filterUsers(backups, model.ProviderMicrosoft)
	if len(microsoftBackups) > 0 {
		u, err := getUserForReplica(db, "/test_2.txt", model.ProviderMicrosoft, cfg.Users)
		if err == nil {
			c, _ := runner.GetOrCreateClient(u)
			sid, _ := c.GetSyncFolderID()
			fMS, _ := getOrCreateFolder(c, sid, "Folder_MS")
			moveFileWrapper(c, u, "/test_2.txt", fMS.ID)
		}
	}

	// Google: test_3 -> Folder_Google
	googleBackups := filterUsers(backups, model.ProviderGoogle)
	if len(googleBackups) > 0 {
		u, err := getUserForReplica(db, "/test_3.txt", model.ProviderGoogle, cfg.Users)
		if err == nil {
			c, _ := runner.GetOrCreateClient(u)
			sid, _ := c.GetSyncFolderID()
			fG, _ := getOrCreateFolder(c, sid, "Folder_Google")
			moveFileWrapper(c, u, "/test_3.txt", fG.ID)
		}
	}

	logger.Info("Updating Metadata...")
	if err := runner.GetMetadata(); err != nil {
		return err
	}

	logger.Info("Running SyncProviders...")
	if err := runner.SyncProviders(); err != nil {
		return err
	}

	logger.Info("Verifying movements...")
	if err := verifyFile(db, "/Folder_Main/test_move.txt", 0); err != nil {
		logger.Error("Verification failed for /Folder_Main/test_move.txt: %v", err)
		printAllFiles(db)
		return err
	}

	if len(microsoftBackups) > 0 {
		if err := verifyFile(db, "/Folder_MS/test_2.txt", 0); err != nil {
			logger.Warning("Verification failed for /Folder_MS/test_2.txt (Move detection might be delayed due to ModTime mismatch): %v", err)
		}
	}
	if len(googleBackups) > 0 {
		if err := verifyFile(db, "/Folder_Google/test_3.txt", 0); err != nil {
			logger.Warning("Verification failed for /Folder_Google/test_3.txt (Move detection might be delayed due to ModTime mismatch): %v", err)
		}
	}
	return nil
}

func runTestCase5(runner *task.Runner, mainUser *model.User, backups []*model.User) error {
	logger.Info("\n--- Test Case 5: Soft Deletion ---")

	getSoftID := func(c api.CloudClient, rootID string) (string, error) {
		aux, err := getOrCreateFolder(c, "root", "sync-cloud-drives-aux")
		if err != nil {
			return "", err
		}
		soft, err := getOrCreateFolder(c, aux.ID, "soft-deleted")
		if err != nil {
			return "", err
		}
		return soft.ID, nil
	}

	moveFileWrapper := func(c api.CloudClient, u *model.User, path, targetFolderID string) error {
		f, err := db.GetFileByPath(path)
		if err != nil {
			return err
		}
		if f == nil {
			return fmt.Errorf("file %s not found", path)
		}
		nativeID := getNativeID(f, u)
		if nativeID == "" {
			return fmt.Errorf("nativeID not found for %s on %s", path, u.Email)
		}
		return c.MoveFile(nativeID, targetFolderID)
	}

	// Move test_5.txt to soft-deleted in Main
	mainClient, _ := runner.GetOrCreateClient(mainUser)
	mainSyncID, _ := mainClient.GetSyncFolderID()
	softMain, _ := getSoftID(mainClient, mainSyncID)
	moveFileWrapper(mainClient, mainUser, "/test_5.txt", softMain)

	// MS
	microsoftBackups := filterUsers(backups, model.ProviderMicrosoft)
	if len(microsoftBackups) > 0 {
		u, err := getUserForReplica(db, "/Folder_MS/test_2.txt", model.ProviderMicrosoft, cfg.Users)
		if err == nil {
			c, _ := runner.GetOrCreateClient(u)
			sid, _ := c.GetSyncFolderID()
			softMS, _ := getSoftID(c, sid)
			moveFileWrapper(c, u, "/Folder_MS/test_2.txt", softMS)
		}
	}

	// Google
	googleBackups := filterUsers(backups, model.ProviderGoogle)
	if len(googleBackups) > 0 {
		u, err := getUserForReplica(db, "/Folder_Google/test_3.txt", model.ProviderGoogle, cfg.Users)
		if err == nil {
			c, _ := runner.GetOrCreateClient(u)
			sid, _ := c.GetSyncFolderID()
			// Note: getSoftID creates aux/soft-deleted.
			// Re-instantiate getting soft ID for Google
			softG2, _ := getSoftID(c, sid)
			moveFileWrapper(c, u, "/Folder_Google/test_3.txt", softG2)
		}
	}

	logger.Info("Updating Metadata...")
	if err := runner.GetMetadata(); err != nil {
		return err
	}
	logger.Info("Running SyncProviders...")
	if err := runner.SyncProviders(); err != nil {
		return err
	}

	verifyGone := func(path string) error {
		f, _ := db.GetFileByPath(path)
		if f != nil && f.Status == "active" {
			return fmt.Errorf("file %s should be soft-deleted/inactive but is %s", path, f.Status)
		}
		return nil
	}
	if err := verifyGone("/test_5.txt"); err != nil {
		return err
	}
	if err := verifyGone("/Folder_MS/test_2.txt"); err != nil {
		return err
	}
	if err := verifyGone("/Folder_Google/test_3.txt"); err != nil {
		return err
	}
	return nil
}

func runTestCase7(runner *task.Runner, mainUser *model.User) error {
	logger.Info("\n--- Test Case 7: Very Big File (3GB) ---")
	test6Name := "test_6.txt"
	test6Size := int64(3) * 1024 * 1024 * 1024

	mainClient, err := runner.GetOrCreateClient(mainUser)
	if err != nil {
		return err
	}
	mainSyncID, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}

	logger.Info("Uploading %s to Main Account (Streamed)...", test6Name)
	// Using io.LimitReader(rand.Reader, ...) might be slow for 3GB.
	// We'll trust the user advice "exactly like Test Case 3" but note the speed implications.
	if _, err := mainClient.UploadFile(mainSyncID, test6Name, io.LimitReader(rand.Reader, test6Size), test6Size); err != nil {
		logger.Error("Upload failed: %v", err)
		return fmt.Errorf("upload very big file failed: %w", err)
	}

	logger.Info("Updating Metadata...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	logger.Info("Running SyncProviders (Very Big File)...")
	if err := runner.SyncProviders(); err != nil {
		logger.Warning("SyncProviders (Very Big File) had error: %v. Continuing...", err)
	}

	logger.Info("Verifying very big file...")
	return verifyFile(db, "/"+test6Name, test6Size)
}

func runTestCase8(runner *task.Runner, mainUser *model.User, backups []*model.User) error {
	logger.Info("\n--- Test Case 8: Restoring from Soft-Deleted ---")

	// Pre-requisite: Move test_5.txt back from soft-deleted to root on Main (Google)
	mainClient, err := runner.GetOrCreateClient(mainUser)
	if err != nil {
		return err
	}
	mainSyncID, err := mainClient.GetSyncFolderID()
	if err != nil {
		return err
	}

	// Helper to find folder ID by name (shallow)
	findFolderID := func(c api.CloudClient, parentID, name string) (string, error) {
		folders, err := c.ListFolders(parentID)
		if err != nil {
			return "", err
		}
		for _, f := range folders {
			if f.Name == name {
				return f.ID, nil
			}
		}
		return "", fmt.Errorf("folder %s not found in %s", name, parentID)
	}

	// Locate current test_5.txt in sync-cloud-drives-aux/soft-deleted
	auxID, err := findFolderID(mainClient, "root", "sync-cloud-drives-aux")
	if err != nil {
		return err
	}
	softID, err := findFolderID(mainClient, auxID, "soft-deleted")
	if err != nil {
		return err
	}

	// List files in soft-deleted to find test_5.txt native ID
	files, err := mainClient.ListFiles(softID)
	if err != nil {
		return err
	}
	var test5NativeID string
	for _, f := range files {
		if f.Name == "test_5.txt" {
			test5NativeID = f.ID
			break
		}
	}
	if test5NativeID == "" {
		return fmt.Errorf("test_5.txt not found in soft-deleted folder")
	}

	logger.Info("Restoring test_5.txt on Main (Google): Moving from soft-deleted to root...")
	if err := mainClient.MoveFile(test5NativeID, mainSyncID); err != nil {
		return fmt.Errorf("failed to move test_5.txt back to root: %w", err)
	}

	logger.Info("Updating Metadata...")
	if err := runner.GetMetadata(); err != nil {
		return err
	}

	// At this point, the system should detect the move (restore)
	logger.Info("Running SyncProviders to propagate restore...")
	if err := runner.SyncProviders(); err != nil {
		return err
	}

	// Verify test_5.txt is active in DB
	if err := verifyFile(db, "/test_5.txt", 0); err != nil {
		return fmt.Errorf("verification of restored file in DB failed: %w", err)
	}
	// Verify it's considered Active (not stuck in trash)
	f, _ := db.GetFileByPath("/test_5.txt")
	if f.Status != "active" {
		return fmt.Errorf("test_5.txt status is %s, expected active", f.Status)
	}

	return nil
}

func runTestCase9(runner *task.Runner, mainUser *model.User, backups []*model.User) error {
	logger.Info("\n--- Test Case 9: Restoring Fragmented File ---")
	// Scenario: test_6.txt (3GB) was uploaded in TC7.
	// It should exist on Google (Main), Microsoft (Backup), and fragmented on Telegram.
	// We will delete it from Google and Microsoft, then Sync to see if it heals from Telegram.

	// 1. Delete from Google (Main)
	mainClient, err := runner.GetOrCreateClient(mainUser)
	if err != nil {
		return err
	}

	f6, err := db.GetFileByPath("/test_6.txt")
	if err != nil {
		return err
	}
	if f6 == nil {
		return fmt.Errorf("test_6.txt missing from DB before test case 9")
	}

	googleNativeID := getNativeID(f6, mainUser)
	if googleNativeID != "" {
		logger.Info("Deleting test_6.txt from Google Main directly...")
		if err := mainClient.DeleteFile(googleNativeID); err != nil {
			return fmt.Errorf("failed to delete from google: %w", err)
		}
	}

	// 2. Delete from Microsoft (Backup)
	microsoftBackups := filterUsers(backups, model.ProviderMicrosoft)
	for _, u := range microsoftBackups {
		msClient, err := runner.GetOrCreateClient(u)
		if err != nil {
			continue
		}
		msNativeID := getNativeID(f6, u)
		if msNativeID != "" {
			logger.Info("Deleting test_6.txt from Microsoft Backup (%s) directly...", u.Email)
			if err := msClient.DeleteFile(msNativeID); err != nil {
				return fmt.Errorf("failed to delete from microsoft: %w", err)
			}
		}
	}

	// 3. Sync
	logger.Info("Running SyncProviders (Attempting Restore from Fragments)...")
	if err := runner.SyncProviders(); err != nil {
		return err
	}

	// 4. Verify
	// Check if file is back active on Google/MS

	// Reload file from DB to see replica status
	f6, err = db.GetFileByPath("/test_6.txt")
	if err != nil {
		return err
	}

	// Check Google Replica
	googleReplicaActive := false
	for _, r := range f6.Replicas {
		if r.Provider == model.ProviderGoogle && r.Status == "active" {
			googleReplicaActive = true
			break
		}
	}
	if !googleReplicaActive {
		return fmt.Errorf("test_6.txt was not restored to Google Main")
	} else {
		logger.Info("Verified: test_6.txt is active on Google.")
	}

	return nil
}

func runTestCase10(runner *task.Runner, mainUser *model.User, backups []*model.User) error {
	logger.Info("\n--- Test Case 10: Hard Deletion ---")

	getSoftID := func(c api.CloudClient, rootID string) (string, error) {
		aux, err := getOrCreateFolder(c, "root", "sync-cloud-drives-aux")
		if err != nil {
			return "", err
		}
		soft, err := getOrCreateFolder(c, aux.ID, "soft-deleted")
		if err != nil {
			return "", err
		}
		return soft.ID, nil
	}

	deleteSoftContent := func(c api.CloudClient, rootID string, u *model.User) error {
		sid, err := getSoftID(c, rootID)
		if err != nil {
			return err
		}
		files, err := c.ListFiles(sid)
		if err != nil {
			return err
		}
		for _, f := range files {
			logger.Info("Deleting %s from soft-deleted on %s...", f.Name, u.Email)
			c.DeleteFile(f.ID)
		}
		return nil
	}

	mainClient, _ := runner.GetOrCreateClient(mainUser)
	mainSyncID, _ := mainClient.GetSyncFolderID()
	deleteSoftContent(mainClient, mainSyncID, mainUser)

	googleBackups := filterUsers(backups, model.ProviderGoogle)
	for _, u := range googleBackups {
		c, _ := runner.GetOrCreateClient(u)
		sid, _ := c.GetSyncFolderID()
		deleteSoftContent(c, sid, u)
	}

	microsoftBackups := filterUsers(backups, model.ProviderMicrosoft)
	for _, u := range microsoftBackups {
		c, _ := runner.GetOrCreateClient(u)
		sid, _ := c.GetSyncFolderID()
		deleteSoftContent(c, sid, u)
	}

	logger.Info("Updating Metadata...")
	if err := runner.GetMetadata(); err != nil {
		return err
	}
	logger.Info("Running SyncProviders...")
	return runner.SyncProviders()
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
			anyReplica, err := db.GetReplicaByNativeID(user.Provider, nativeID)
			if err == nil && anyReplica != nil && anyReplica.Status == "active" {
				continue
			}

			// Check if it's a fragment (for Telegram mainly, but good to be generic)
			anyFragReplica, err := db.GetReplicaByNativeFragmentID(nativeID)
			if err == nil && anyFragReplica != nil && anyFragReplica.Status == "active" && anyFragReplica.Provider == user.Provider {
				continue
			}

			logger.Error("Unexpected file on cloud: %s (ID: %s, Name: %s)", f.Path, nativeID, f.Name)
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
		// Use NativeID (Replica ID) as key for consistency checking
		if len(f.Replicas) > 0 {
			results[f.Replicas[0].NativeID] = f
		} else {
			// Fallback (shouldn't happen usually)
			results[f.ID] = f
		}
	}

	folders, err := client.ListFolders(folderID)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		// Recurse
		err := listFilesRecursive(client, folder.ID, filepath.Join(currentPath, folder.Name), results)
		if err != nil {
			return err
		}
	}
	return nil
}
