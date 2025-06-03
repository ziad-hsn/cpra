package parser

import (
	"cpra/internal/loader/schema"
	"io"
)

type Parser interface {
	Parse(r io.Reader) (schema.Manifest, error)
}

func NewParser() Parser {
	yamlParser := NewYamlParser()
	return yamlParser
}
