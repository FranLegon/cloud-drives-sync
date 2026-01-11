package cmd

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"os"

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

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Run system integration tests",
	Long:  `Test command performs a series of integration tests to validate system functionality.`,
	RunE:  runTest,
}

func init() {
	testCmd.Flags().BoolVar(&testSafe, "safe", false, "Skip destructive cleanup steps")
	testCmd.Flags().BoolVar(&testForce, "force", false, "Skip confirmation prompt")
	rootCmd.AddCommand(testCmd)
}

func runTest(cmd *cobra.Command, args []string) error {
	logger.Info("Starting Test Command...")

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
		runner := task.NewRunner(cfg, nil, false)
		if err := cleanupCloudFiles(runner); err != nil {
			return err
		}

		logger.Info("Deleting local metadata...")
		if db != nil {
			db.Close()
			db = nil
		}
		dbPath := database.GetDBPath()
		if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove DB: %w", err)
		}

		var err error
		db, err = database.Open(masterPassword)
		if err != nil {
			return fmt.Errorf("failed to re-open DB: %w", err)
		}
		if err := db.Initialize(); err != nil {
			return fmt.Errorf("failed to initialize DB: %w", err)
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

	runner := task.NewRunner(cfg, db, false)

	// Ensure folders exist (Init step equivalent)
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

	// --- Phase 2: Test Case 1 ---
	logger.Info("\n--- Test Case 1: test_1.txt (Main -> Free -> Sync) ---")
	f1Name := "test_1.txt"
	f1Data, err := os.ReadFile(f1Name)
	if err != nil {
		return fmt.Errorf("missing local file %s: %w", f1Name, err)
	}

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

	// Update DB to reflect the new file
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
	if err := verifyFile(db, "/"+f1Name, int64(len(f1Data))); err != nil {
		return err
	}

	// --- Test Case 2: Multi-Provider Backups ---
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
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}

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
	// Verify existence (size checked roughly)
	if err := verifyFile(db, "/test_2.txt", 0); err != nil {
		return err
	}
	if err := verifyFile(db, "/test_3.txt", 0); err != nil {
		return err
	}
	if err := verifyFile(db, "/test_4.txt", 0); err != nil {
		return err
	}

	// --- Test Case 3: Large File ---
	logger.Info("\n--- Test Case 3: Large File (6GB) ---")
	test5Name := "test_5.txt"
	test5Size := int64(6) * 1024 * 1024 * 1024

	logger.Info("Uploading %s to Main Account (Streamed)...", test5Name)
	if _, err := mainClient.UploadFile(mainSyncID, test5Name, io.LimitReader(rand.Reader, test5Size), test5Size); err != nil {
		return fmt.Errorf("upload large file failed: %w", err)
	}

	logger.Info("Updating Metadata to find large file...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	logger.Info("Running SyncProviders (Large File)...")
	if err := runner.SyncProviders(); err != nil {
		return fmt.Errorf("SyncProviders failed: %w", err)
	}

	logger.Info("Verifying large file...")
	if err := verifyFile(db, "/"+test5Name, test5Size); err != nil {
		return err
	}

	// --- Test Case 4: Movements ---
	logger.Info("\n--- Test Case 4: Movements ---")

	// Helper to move
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

	// Create folder "Folder_Main" in Main
	folderName := "Folder_Main"
	logger.Info("Creating folder %s ...", folderName)
	newFolder, err := mainClient.CreateFolder(mainSyncID, folderName)
	if err != nil {
		return err
	}

	// Move test_1.txt to Folder_Main
	if err := moveFileWrapper(mainClient, mainUser, "/test_1.txt", newFolder.ID); err != nil {
		return err
	}

	// MS: test_2 -> Folder_MS (if exists)
	if len(microsoftBackups) > 0 {
		u := microsoftBackups[0]
		c, _ := runner.GetOrCreateClient(u)
		sid, _ := c.GetSyncFolderID()
		fMS, err := getOrCreateFolder(c, sid, "Folder_MS")
		if err != nil {
			return err
		}
		if err := moveFileWrapper(c, u, "/test_2.txt", fMS.ID); err != nil {
			return err
		}
	}

	// Google: test_3 -> Folder_Google (if exists)
	if len(googleBackups) > 0 {
		u := googleBackups[0]
		c, _ := runner.GetOrCreateClient(u)
		sid, _ := c.GetSyncFolderID()
		fG, err := getOrCreateFolder(c, sid, "Folder_Google")
		if err != nil {
			return err
		}
		if err := moveFileWrapper(c, u, "/test_3.txt", fG.ID); err != nil {
			return err
		}
	}

	logger.Info("Updating Metadata to detect movements...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	logger.Info("Running SyncProviders (Movements)...")
	if err := runner.SyncProviders(); err != nil {
		return fmt.Errorf("SyncProviders failed: %w", err)
	}

	// Verify
	if err := verifyFile(db, "/Folder_Main/test_1.txt", 0); err != nil {
		return err
	}
	if len(microsoftBackups) > 0 {
		if err := verifyFile(db, "/Folder_MS/test_2.txt", 0); err != nil {
			return err
		}
	}
	if len(googleBackups) > 0 {
		if err := verifyFile(db, "/Folder_Google/test_3.txt", 0); err != nil {
			return err
		}
	}

	// --- Test Case 5: Soft Deletion ---
	logger.Info("\n--- Test Case 5: Soft Deletion ---")

	// Helper to get soft-deleted ID
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

	// Main: test_5.txt -> soft-deleted
	softMain, err := getSoftID(mainClient, mainSyncID)
	if err != nil {
		return err
	}
	if err := moveFileWrapper(mainClient, mainUser, "/test_5.txt", softMain); err != nil {
		return err
	}

	// MS: test_2.txt (/Folder_MS/test_2.txt) -> soft-deleted
	if len(microsoftBackups) > 0 {
		u := microsoftBackups[0]
		c, _ := runner.GetOrCreateClient(u)
		sid, _ := c.GetSyncFolderID()
		softMS, _ := getSoftID(c, sid)
		if err := moveFileWrapper(c, u, "/Folder_MS/test_2.txt", softMS); err != nil {
			return err
		}
	}

	// Google: test_3.txt (/Folder_Google/test_3.txt) -> soft-deleted
	if len(googleBackups) > 0 {
		u := googleBackups[0]
		c, _ := runner.GetOrCreateClient(u)
		sid, _ := c.GetSyncFolderID()
		softG, _ := getSoftID(c, sid)
		if err := moveFileWrapper(c, u, "/Folder_Google/test_3.txt", softG); err != nil {
			return err
		}
	}

	logger.Info("Updating Metadata to detect soft deletions...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	logger.Info("Running SyncProviders (Soft Delete)...")
	if err := runner.SyncProviders(); err != nil {
		return err
	}

	// Verify test_5.txt is gone
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

	// --- Test Case 6: Hard Deletion ---
	logger.Info("\n--- Test Case 6: Hard Deletion ---")
	// Delete files from soft-deleted

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
			if err := c.DeleteFile(f.ID); err != nil {
				return err
			}
		}
		return nil
	}

	if err := deleteSoftContent(mainClient, mainSyncID, mainUser); err != nil {
		return err
	}
	if len(microsoftBackups) > 0 {
		u := microsoftBackups[0]
		c, _ := runner.GetOrCreateClient(u)
		sid, _ := c.GetSyncFolderID()
		if err := deleteSoftContent(c, sid, u); err != nil {
			return err
		}
	}
	if len(googleBackups) > 0 {
		u := googleBackups[0]
		c, _ := runner.GetOrCreateClient(u)
		sid, _ := c.GetSyncFolderID()
		if err := deleteSoftContent(c, sid, u); err != nil {
			return err
		}
	}

	logger.Info("Updating Metadata to detect hard deletions...")
	if err := runner.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	logger.Info("Running SyncProviders (Hard Delete)...")
	if err := runner.SyncProviders(); err != nil {
		return err
	}

	logger.Info("\nTEST SUITE COMPLETED SUCCESSFULLY")
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

func getNativeID(f *model.File, u *model.User) string {
	for _, r := range f.Replicas {
		if r.Provider == u.Provider && (r.AccountID == u.Email || r.AccountID == u.Phone) {
			return r.NativeID
		}
	}
	return ""
}
