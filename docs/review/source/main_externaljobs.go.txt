//go:build externaljobs

package main

import (
	"github.com/ziad-hsn/cpra/internal/httpserver"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func configureExternalJobsServer(config *httpserver.ServerConfig, settings runtimeconfig.Config) {
	config.ExternalJobsEnabled = settings.ExternalJobs.Enabled
}

func configureCatalogOptions(options *management.CatalogOptions, settings runtimeconfig.Config) {
	options.ExternalJobsEnabled = settings.ExternalJobs.Enabled
}
