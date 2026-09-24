//go:build !js

// Native oracle for the isolated browser-parser qualification harness.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func main() {
	items := []string{}
	positions := []collection.Location{}
	err := collection.NormalizeFile(context.Background(), os.Stdin, collection.FileNormalizationProfile, collection.DecodeOptions{SourceName: "source"}, func(item collection.NormalizedItem) error {
		defer clear(item.JSON)
		items = append(items, string(item.JSON))
		positions = append(positions, item.Location)
		return nil
	})
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"valid": false})
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"valid": true, "profile": collection.FileNormalizationProfile, "items": items, "positions": positions})
}
