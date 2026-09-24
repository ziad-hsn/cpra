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
				if !args[1].Invoke(item.ID, item.Location.Document, item.Location.Item, string(item.JSON)).Bool() {
					return errors.New("browser capacity exceeded")
				}
				return nil
			})
		result = map[string]any{"valid": err == nil}
		if err != nil {
			// Only numeric attribution crosses this boundary. Do not expose err.
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
