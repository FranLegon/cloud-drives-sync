package cmd

import (
	"fmt"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
)

// PreFlightCheck enforces the 'synched-cloud-drives' folder constraint for all accounts.
func PreFlightCheck(googleSvc google.GoogleDrive, msSvc microsoft.OneDrive, mainGoogleEmail, mainMicrosoftEmail string) error {
	// Google
	if googleSvc != nil && mainGoogleEmail != "" {
		folders, err := googleSvc.ListFolders(mainGoogleEmail, "synched-cloud-drives")
		if err != nil {
			return fmt.Errorf("[Google][%s] Error listing folders: %v", mainGoogleEmail, err)
		}
		if len(folders) != 1 {
			return fmt.Errorf("[Google][%s] Pre-flight failed: found %d 'synched-cloud-drives' folders. Resolve manually.", mainGoogleEmail, len(folders))
		}
		if !folders[0].IsRoot {
			if err := googleSvc.MoveFolderToRoot(mainGoogleEmail, folders[0].ID); err != nil {
				return fmt.Errorf("[Google][%s] Failed to move folder to root: %v", mainGoogleEmail, err)
			}
		}
	}
	// Microsoft
	if msSvc != nil && mainMicrosoftEmail != "" {
		folders, err := msSvc.ListFolders(mainMicrosoftEmail, "synched-cloud-drives")
		if err != nil {
			return fmt.Errorf("[Microsoft][%s] Error listing folders: %v", mainMicrosoftEmail, err)
		}
		if len(folders) != 1 {
			return fmt.Errorf("[Microsoft][%s] Pre-flight failed: found %d 'synched-cloud-drives' folders. Resolve manually.", mainMicrosoftEmail, len(folders))
		}
		if !folders[0].IsRoot {
			if err := msSvc.MoveFolderToRoot(mainMicrosoftEmail, folders[0].ID); err != nil {
				return fmt.Errorf("[Microsoft][%s] Failed to move folder to root: %v", mainMicrosoftEmail, err)
			}
		}
	}
	return nil
}
