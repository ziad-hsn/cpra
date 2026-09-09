package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

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
	b, err := yaml.Marshal(v)
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

// fmtDur renders a duration in milliseconds with one decimal, or "-" for zero.
func fmtDur(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%dns", d.Nanoseconds())
	}
	return fmt.Sprintf("%.1fms", float64(d.Nanoseconds())/1e6)
}

// fmtAgo renders a time as a relative age like "3m2s", or "-" if unset/invalid.
func fmtAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d >= time.Second:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		return "<1s"
	}
}

// fmtNext renders the scheduled time without treating it as a past age.
func fmtNext(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

// fmtRate renders a per-second rate, or "-" for zero.
func fmtRate(f float64) string {
	if f <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f/s", f)
}

// strOrDash returns "-" for an empty string.
func strOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
