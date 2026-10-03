//go:build !externaljobs

package main

import (
	"github.com/ziad-hsn/cpra/internal/httpserver"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func configureExternalJobsServer(*httpserver.ServerConfig, runtimeconfig.Config) {}

func configureCatalogOptions(*management.CatalogOptions, runtimeconfig.Config) {}
