package output

import (
	"encoding/json"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Formatter prints arbitrary data in desired output format.
type Formatter interface {
	Format(data any) error
}

// New returns a Formatter based on format string.
// Supported: table (default), json, yaml, wide.
func New(format string, w io.Writer) Formatter {
	switch format {
	case "json":
		return &jsonFormatter{w: w}
	case "yaml", "yml":
		return &yamlFormatter{w: w}
	case "wide":
		return &tableFormatter{w: w, wide: true}
	default:
		return &tableFormatter{w: w}
	}
}

// ---------------- JSON --------------------

type jsonFormatter struct {
	w io.Writer
}

func (j *jsonFormatter) Format(v any) error {
	enc := json.NewEncoder(j.w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---------------- YAML --------------------

type yamlFormatter struct {
	w io.Writer
}

func (y *yamlFormatter) Format(v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(y.w, string(data))
	return err
}
