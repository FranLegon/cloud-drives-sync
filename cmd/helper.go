package cmd

import (
	"fmt"
	
	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
)

func getClientForFile(runner *task.Runner, file *model.File) (api.CloudClient, error) {
	// Use the first replica if file has replicas
	if len(file.Replicas) > 0 {
		return getClientForReplica(runner, file.Replicas[0])
	}
	
	// If no replicas, we can't determine which client to use
	return nil, fmt.Errorf("file has no replicas, cannot determine client")
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
