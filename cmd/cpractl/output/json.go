package output

import (
    "encoding/json"
    "io"
)

type JSONFormatter struct {
    w io.Writer
}

func (j *JSONFormatter) Format(v any) error {
    enc := json.NewEncoder(j.w)
    enc.SetIndent("", "  ")
    return enc.Encode(v)
}
