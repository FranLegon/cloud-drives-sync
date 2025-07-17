
package microsoft

type OneDrive interface {
	PreFlightCheck(mainEmail string) error
	CreateSyncFolder(mainEmail string) error
	ShareSyncFolder(mainEmail, backupEmail string) error
	ListFilesInSyncFolder(email string) ([]OneDriveFile, error)
	ListFolders(email, folderName string) ([]OneDriveFolder, error)
	MoveFolderToRoot(email, folderID string) error
	UploadFile(email, path string, content []byte) error
	DownloadFile(email, fileID string) ([]byte, error)
	DeleteFile(email, fileID string) error
	GetQuota(email string) (used, total int64, err error)
	TransferOwnership(fileID, fromEmail, toEmail string) error
	CheckToken(email string) error
}

type OneDriveFolder struct {
	ID     string
	Name   string
	IsRoot bool
}

type OneDriveFile struct {
	ID       string
	Name     string
	Hash     string
	Size     int64
	MimeType string
	ParentID string
	Created  string
	Modified string
}
