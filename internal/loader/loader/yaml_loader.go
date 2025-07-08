package loader

import (
	"cpra/internal/loader/parser"
	"errors"
	"fmt"
	"strings"

	//"cpra/internal/loader/parser"
	"cpra/internal/loader/schema"
	//"errors"
	//"fmt"
	"gopkg.in/yaml.v3"
	//"strings"

	//"github.com/ziad-hsn/cpra/loader/validator"
	"log"
	"os"
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

func (l *YamlLoader) Load() {
	file, err := os.Open(l.File)
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(file)

	if err != nil {
		log.Fatal(err)
	}
	//decoder := yaml.NewDecoder(file)
	//var manifest schema.Manifest
	//if err := decoder.Decode(&manifest); err != nil {
	//	// This error will now include line numbers and be very clear
	//	// because it comes directly from the yaml.v3 library.
	//	log.Fatal(err)
	//}
	yamlParser := parser.NewYamlParser()
	manifest, err := yamlParser.Parse(file)
	if err != nil {
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			for _, msg := range typeErr.Errors {
				if strings.HasPrefix(msg, "line") {
					fatalManifestError(fmt.Errorf("invalid manifest: %s", msg))
				}
			}
		}
		fatalManifestError(fmt.Errorf("invalid manifest: %w", err))
	}
	//yamlValidator := validator.NewYamlValidator()
	//err = yamlValidator.ValidateManifest(&manifest)
	//if err != nil {
	//	log.Fatal(err)
	//}
	l.Manifest = manifest
}

func (l *YamlLoader) GetManifest() schema.Manifest {
	return l.Manifest
}
