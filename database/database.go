package database

import (
	_ "github.com/mattn/go-sqlite3"
)

type FileRecord struct {
	FileID           string
	Provider         string
	OwnerEmail       string
	FileHash         string
	FileName         string
	FileSize         int64
	FileExtension    string
	ParentFolderID   string
	ParentFolderName string
	CreatedOn        string
	LastModified     string
	LastSynced       string
}

type Database interface {
	InitDB(path string) error
	InsertOrUpdateFile(file FileRecord) error
	GetDuplicates(provider string) (map[string][]FileRecord, error)
	GetFilesByHash(hash string) ([]FileRecord, error)
	GetLargestFiles(provider, ownerEmail string, limit int) ([]FileRecord, error)
	UpdateOwner(fileID, provider, newOwner string) error
	GetAllFiles(provider string) ([]FileRecord, error)
	Close() error
}
