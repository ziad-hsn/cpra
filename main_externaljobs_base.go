//go:build !externaljobs

package main

import (
	"github.com/ziad-hsn/cpra/internal/httpserver"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func configureExternalJobsServer(*httpserver.ServerConfig, runtimeconfig.Config) {}
