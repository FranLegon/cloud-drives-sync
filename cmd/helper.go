package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
)

func getClientForFile(runner *task.Runner, file *model.File) (api.CloudClient, error) {
	// Try to use owner's client first if available and different from user
	if file.OwnerEmail != "" && file.OwnerEmail != file.UserEmail {
		client, err := runner.GetOrCreateClient(&model.User{
			Provider: file.Provider,
			Email:    file.OwnerEmail,
		})
		if err == nil {
			logger.InfoTagged([]string{string(file.Provider)}, "Using owner account %s for deletion", file.OwnerEmail)
			return client, nil
		}
	}

	// Fallback to user who found the file
	return runner.GetOrCreateClient(&model.User{
		Provider:     file.Provider,
		Email:        file.UserEmail,
		Phone:        file.UserPhone,
		RefreshToken: "", // Will use from config
	})
}
