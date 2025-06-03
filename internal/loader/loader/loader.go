package loader

import (
	"cpra/internal/loader/schema"
)

type Loader interface {
	Load()
	GetManifest() schema.Manifest
}

func NewLoader(loaderType string, filename string) Loader {

	switch loaderType {
	case "yaml":
		yamlLoader := NewYamlLoader(filename)
		return yamlLoader

	default:
		yamlLoader := NewYamlLoader(filename)
		return yamlLoader

	}
}
