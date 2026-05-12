//go:build auto

package main

import (
	_ "embed"

	"github.com/FranLegon/cloud-drives-sync/internal/config"
)

//go:embed config.json.enc
var embeddedConfig []byte

//go:embed config.salt
var embeddedSalt []byte

func init() {
	config.SetEmbeddedData(embeddedConfig, embeddedSalt)
}
