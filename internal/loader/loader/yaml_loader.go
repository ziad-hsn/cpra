package loader

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"cpra/internal/loader/parser"
	"cpra/internal/loader/schema"
)

// YamlLoader implements the Loader interface for YAML-formatted monitor
// configuration files.
//
// YamlLoader parses YAML files containing monitor definitions and extracts
// them into a schema.Manifest structure. It uses a buffered reader and the
// parser package for efficient parsing.
//
// This loader is suitable for smaller configurations. For large files
// (100K+ monitors), consider using the streaming loader instead.
type YamlLoader struct {
	// File is the path to the YAML configuration file to load.
	File string
	// Manifest contains the parsed monitor configuration after Load() succeeds.
	Manifest schema.Manifest
}

// NewYamlLoader creates a new YAML loader for the specified file.
//
// NewYamlLoader initializes a YamlLoader instance but does not perform any
// file I/O. Call Load() to parse the file and populate the Manifest.
//
// Parameters:
//   - fileName: Path to the YAML configuration file to load
//
// Returns:
//   - *YamlLoader: A new loader instance ready to parse the file
//
// Example:
//
//	loader := NewYamlLoader("monitors.yaml")
//	if err := loader.Load(); err != nil {
//		log.Fatal(err)
//	}
//	manifest := loader.GetManifest()
func NewYamlLoader(fileName string) *YamlLoader {
	return &YamlLoader{
		fileName,
		schema.Manifest{},
	}
}

// Load parses the YAML configuration file and populates the Manifest.
//
// Load opens the file specified in File, parses it using a buffered reader
// and the YAML parser, and stores the result in Manifest. It handles YAML
// syntax errors and provides detailed error messages including line numbers.
//
// Returns an error if:
//   - The file cannot be opened or read
//   - The YAML syntax is invalid
//   - The file structure does not match the expected schema
//
// After Load() succeeds, GetManifest() will return the parsed configuration.
//
// Example:
//
//	loader := NewYamlLoader("monitors.yaml")
//	if err := loader.Load(); err != nil {
//		log.Fatalf("Failed to load monitors: %v", err)
//	}
func (l *YamlLoader) Load() error {
	file, err := os.Open(l.File)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	//decoder := yaml.NewDecoder(file)
	//var manifest schema.Manifest
	//if err := decoder.Decode(&manifest); err != nil {
	//	// This error will now include line numbers and be very clear
	//	// because it comes directly from the yaml.v3 library.
	//	log.Fatal(err)
	//}
	yamlParser := parser.NewYamlParser()
	reader := bufio.NewReaderSize(file, 64*1024)
	manifest, err := yamlParser.Parse(reader)
	if err != nil {
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			for _, msg := range typeErr.Errors {
				if strings.HasPrefix(msg, "line") {
					return fmt.Errorf("invalid manifest: %s", msg)
				}
			}
		}
		return fmt.Errorf("invalid manifest: %w", err)
	}
	//yamlValidator := validator.NewYamlValidator()
	//err = yamlValidator.ValidateManifest(&manifest)
	//if err != nil {
	//	log.Fatal(err)
	//}
	l.Manifest = manifest
	return nil
}

// GetManifest returns the parsed monitor configuration manifest.
//
// GetManifest should be called after Load() succeeds. It returns the
// schema.Manifest structure containing all monitor definitions parsed from
// the YAML file.
//
// Returns:
//   - schema.Manifest: The parsed monitor configuration. If Load() has not
//     been called or failed, returns an empty manifest.
//
// Example:
//
//	loader := NewYamlLoader("monitors.yaml")
//	if err := loader.Load(); err != nil {
//		log.Fatal(err)
//	}
//	manifest := loader.GetManifest()
//	for _, monitor := range manifest.Monitors {
//		fmt.Printf("Loaded monitor: %s\n", monitor.Name)
//	}
func (l *YamlLoader) GetManifest() schema.Manifest {
	return l.Manifest
}
