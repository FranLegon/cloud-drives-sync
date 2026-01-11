package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
)

func getClientForFile(runner *task.Runner, file *model.File) (api.CloudClient, error) {
	// For backwards compatibility, use the first replica if file has replicas
	if len(file.Replicas) > 0 {
		return getClientForReplica(runner, file.Replicas[0])
	}
	
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

func getClientForReplica(runner *task.Runner, replica *model.Replica) (api.CloudClient, error) {
	// Get client for the replica's provider and account
	var email, phone string
	if replica.Provider == model.ProviderTelegram {
		phone = replica.AccountID
	} else {
		email = replica.AccountID
	}
	
	return runner.GetOrCreateClient(&model.User{
		Provider:     replica.Provider,
		Email:        email,
		Phone:        phone,
		RefreshToken: "", // Will use from config
	})
}
