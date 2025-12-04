// Package loader provides interfaces and implementations for loading monitor
// configurations from various file formats.
//
// The loader package abstracts the process of parsing monitor configuration files
// (YAML, JSON) and converting them into schema.Manifest structures that can be
// consumed by the controller. This package provides a simple, non-streaming loader
// suitable for smaller configurations.
//
// For large-scale deployments (1M+ monitors), use the streaming loader in
// internal/loader/streaming which provides memory-efficient parsing.
//
// # Loader Types
//
// Currently supported:
//   - YAML: YAML-formatted monitor configuration files
//
// # Usage
//
//	loader := loader.NewLoader("yaml", "monitors.yaml")
//	if err := loader.Load(); err != nil {
//		log.Fatal(err)
//	}
//	manifest := loader.GetManifest()
//
// # Streaming Alternative
//
// For large files, use the streaming loader:
//
//	import "cpra/internal/loader/streaming"
//	loader := streaming.NewStreamingLoader("monitors.yaml", world, config, mapper)
//	stats, err := loader.Load(ctx)
package loader

import (
	"cpra/internal/loader/schema"
)

// Loader defines the interface for loading monitor configurations from files.
//
// Loader implementations parse configuration files and extract monitor definitions
// into a schema.Manifest structure. The Load() method performs the actual parsing,
// and GetManifest() returns the parsed configuration.
//
// Implementations should handle file I/O errors and format validation errors
// gracefully, returning descriptive error messages.
type Loader interface {
	// Load parses the configuration file and populates the internal manifest.
	// Returns an error if file reading or parsing fails.
	Load() error

	// GetManifest returns the parsed monitor configuration manifest.
	// Should be called after Load() succeeds.
	GetManifest() schema.Manifest
}

// NewLoader creates a new loader instance for the specified file type.
//
// NewLoader is a factory function that returns an appropriate loader implementation
// based on the loaderType parameter. Currently, only "yaml" is supported, and
// the default behavior is to use YAML loading.
//
// Parameters:
//   - loaderType: Type of loader to create ("yaml" is currently supported)
//   - filename: Path to the configuration file to load
//
// Returns:
//   - Loader: A loader instance ready to parse the specified file
//
// Example:
//
//	loader := NewLoader("yaml", "monitors.yaml")
//	if err := loader.Load(); err != nil {
//		log.Fatal(err)
//	}
func NewLoader(loaderType string, filename string) Loader {
	switch loaderType {
	case "yaml":
		yamlLoader := NewYamlLoader(filename)
		return yamlLoader

	default:
		// Default to YAML loader for backward compatibility
		yamlLoader := NewYamlLoader(filename)
		return yamlLoader
	}
}
