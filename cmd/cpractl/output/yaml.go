package output

import (
    "fmt"
    "io"

    "gopkg.in/yaml.v3"
)

type YAMLFormatter struct {
    w io.Writer
}

func (y *YAMLFormatter) Format(v any) error {
    data, err := yaml.Marshal(v)
    if err != nil {
        return err
    }
    _, err = fmt.Fprintln(y.w, string(data))
    return err
}
