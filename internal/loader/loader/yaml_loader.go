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

type YamlLoader struct {
	File     string
	Manifest schema.Manifest
}

func NewYamlLoader(fileName string) *YamlLoader {
	return &YamlLoader{
		fileName,
		schema.Manifest{},
	}
}

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

func (l *YamlLoader) GetManifest() schema.Manifest {
	return l.Manifest
}
