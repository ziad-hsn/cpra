package output

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
)

// tableFormatter formats data as a table.
type tableFormatter struct {
	w    io.Writer
	wide bool
}

func (t *tableFormatter) Format(data any) error {
	switch v := data.(type) {
	case TableData:
		t.renderTable(v)
		return nil
	default:
		_, err := fmt.Fprintf(t.w, "%+v\n", v)
		return err
	}
}

// TableData holds table headers and rows for rendering.
type TableData struct {
	Headers []string
	Rows    [][]string
}

func (t *tableFormatter) renderTable(d TableData) {
	tw := tabwriter.NewWriter(t.w, 0, 0, 2, ' ', 0)
	
	// Print headers
	fmt.Fprintln(tw, strings.Join(d.Headers, "\t"))
	
	// Print rows
	for _, row := range d.Rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	
	tw.Flush()
}

// StatusColor returns a colorized status string.
func StatusColor(status string) string {
	switch strings.ToLower(status) {
	case "healthy", "enabled":
		return color.HiGreenString(status)
	case "degraded", "warning":
		return color.HiYellowString(status)
	case "unhealthy", "disabled", "error":
		return color.HiRedString(status)
	default:
		return status
	}
}
