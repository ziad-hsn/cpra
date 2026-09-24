package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// printer renders command output in the selected format. JSON and YAML marshal
// a value directly; table and wide render headers+rows via a tabwriter. The
// writer comes from cmd.OutOrStdout() so callers (and tests) can redirect it.
type printer struct {
	out    io.Writer
	format string
}

func newPrinter(cmd *cobra.Command, o *options) *printer {
	return &printer{out: cmd.OutOrStdout(), format: o.output}
}

// wide reports whether the wide output format is selected.
func (p *printer) wide() bool { return p.format == formatWide }

// render dispatches on the output format. tableFn supplies the table/wide
// rendering; it is used for both "table" and "wide" (the caller varies columns).
func (p *printer) render(v interface{}, tableFn func(w io.Writer) error) error {
	switch p.format {
	case formatJSON:
		return p.JSON(v)
	case formatYAML:
		return p.YAML(v)
	default:
		return tableFn(p.out)
	}
}

// JSON writes v as indented JSON.
func (p *printer) JSON(v interface{}) error {
	enc := json.NewEncoder(p.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// YAML writes v as YAML.
func (p *printer) YAML(v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	defer clear(raw)
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return err
	}
	b, err := yaml.Marshal(&node)
	if err != nil {
		return err
	}
	_, err = p.out.Write(b)
	return err
}

// table writes a columnar table from headers and rows, using the tab writer
// for alignment (columns separated by a single tab).
func table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

// kv writes a simple KEY: value listing for single-object table output.
func kv(w io.Writer, rows [][2]string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(tw, "%s:\t%s\n", r[0], r[1])
	}
	return tw.Flush()
}

// fmtBool renders a bool compactly.
func fmtBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// strOrDash returns "-" for an empty string.
func strOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
