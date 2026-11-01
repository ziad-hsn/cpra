//go:build js && wasm && !externaljobs

// The browser parser is deliberately built from the public SDK's base contract.
// It registers no transports, plugins, provider jobs, filesystem or URL loaders.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"runtime/debug"
	"strconv"
	"syscall/js"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

var documentPosition = regexp.MustCompile(`(?:^| )document ([0-9]+)(?: item ([0-9]+))?`)

func main() {
	js.Global().Set("cpraCollectionNormalizationProfile", collection.FileNormalizationProfile)
	// Static metadata from the exact linked binary; no input or parser invocation.
	if information, ok := debug.ReadBuildInfo(); ok {
		if raw, err := json.Marshal(information); err == nil {
			js.Global().Set("cpraCollectionBuildJSON", string(raw))
		}
	}
	decode := js.FuncOf(func(_ js.Value, args []js.Value) (result any) {
		// A bridge/native failure is never formatted with source or provider data.
		defer func() {
			if recover() != nil {
				result = map[string]any{"valid": false}
			}
		}()
		if len(args) != 2 || !args[0].InstanceOf(js.Global().Get("Uint8Array")) || args[1].Type() != js.TypeFunction {
			return map[string]any{"valid": false}
		}
		size := args[0].Get("byteLength").Int()
		if size < 0 || size > 64<<20 {
			return map[string]any{"valid": false}
		}
		source := make([]byte, size)
		defer clear(source)
		js.CopyBytesToGo(source, args[0])
		err := collection.NormalizeFile(context.Background(), bytes.NewReader(source), collection.FileNormalizationProfile,
			collection.DecodeOptions{SourceName: "source"}, func(item collection.NormalizedItem) error {
				defer clear(item.JSON)
				coordinates, err := json.Marshal(struct {
					Line   int                                  `json:"line,omitempty"`
					Column int                                  `json:"column,omitempty"`
					Fields map[string]collection.SourcePosition `json:"fields,omitempty"`
				}{item.Location.Line, item.Location.Column, item.FieldPositions})
				if err != nil {
					return errors.New("source coordinates unavailable")
				}
				var resource api.Resource
				defer func() { clear(resource.Spec); clear(resource.Status) }()
				if err := api.StrictDecode(item.JSON, &resource); err != nil {
					return errors.New("normalized resource unavailable")
				}
				warnings := api.ResourceWarnings(resource)
				if warnings == nil {
					warnings = []api.FieldError{}
				}
				advisories, err := json.Marshal(warnings)
				if err != nil || len(advisories) > 16384 {
					return errors.New("configuration advisories unavailable")
				}
				if !args[1].Invoke(item.ID, item.Location.Document, item.Location.Item, string(item.JSON), string(coordinates), string(advisories)).Bool() {
					return errors.New("browser capacity exceeded")
				}
				return nil
			})
		result = map[string]any{"valid": err == nil}
		if err != nil {
			// Only coordinates and reviewed validation rules cross this boundary.
			var source *collection.SourceError
			if errors.As(err, &source) {
				for key, value := range map[string]int{"document": source.Location.Document, "item": source.Location.Item, "line": source.Location.Line, "column": source.Location.Column} {
					if value > 0 && value <= 100_000_000 {
						result.(map[string]any)[key] = value
					}
				}
			}
			if field, ok := api.ValidationFieldError(err); ok {
				result.(map[string]any)["field"] = field.Field
				if source != nil && api.SafeValidationField(source.Field) != "" {
					result.(map[string]any)["field"] = source.Field
				}
				result.(map[string]any)["reason"] = field.Reason
			}
			// Older parser errors expose only numeric document/item coordinates.
			if match := documentPosition.FindStringSubmatch(err.Error()); match != nil {
				for i, field := range []string{"document", "item"} {
					if n, e := strconv.Atoi(match[i+1]); e == nil && n > 0 && n <= 10_000_000 {
						result.(map[string]any)[field] = n
					}
				}
			}
		}
		return result
	})
	js.Global().Set("cpraDecodeCollection", decode)
	select {}
}
