package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func managementInputReader(cmd *cobra.Command, path string) (io.ReadCloser, error) {
	if path == "" {
		return nil, errors.New("an input file is required; use - to read stdin")
	}
	if path == "-" {
		return io.NopCloser(cmd.InOrStdin()), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, localFileFailure("selected input file", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("input must be a regular file, or - for stdin")
	}
	return file, nil
}

func readManagementResource(cmd *cobra.Command, path string) (api.Resource, error) {
	var resource api.Resource
	if err := cmd.Context().Err(); err != nil {
		return resource, managementFailure(err)
	}
	reader, err := managementInputReader(cmd, path)
	if err != nil {
		return resource, err
	}
	label := path
	if label == "-" {
		label = "stdin"
	}
	if len(label) > 256 {
		label = "resource input"
	}
	count := 0
	err = collection.Decode(cmd.Context(), reader, collection.DecodeOptions{SourceName: label, MaxBytes: api.MaxResourceBytes, MaxResourceBytes: api.MaxResourceBytes, MaxDocumentBytes: api.MaxResourceBytes, MaxResources: 1}, func(item collection.Item) error {
		count++
		resource = item.Resource
		return item.ValidationError(api.ValidateResourceSettings(resource))
	})
	closeErr := reader.Close()
	if contextErr := cmd.Context().Err(); contextErr != nil {
		clear(resource.Spec)
		return api.Resource{}, managementFailure(contextErr)
	}
	if err != nil || closeErr != nil || count != 1 {
		if _, ok := api.ValidationFieldError(err); ok {
			clear(resource.Spec)
			return api.Resource{}, managementFailure(errors.Join(cpra.ErrInvalid, err))
		}
		clear(resource.Spec)
		return api.Resource{}, &managementCommandError{"input must contain exactly one valid resource no larger than 1 MiB; collection apply is a separate operation", cpra.ErrInvalid}
	}
	return resource, nil
}

func readManagementPatch(cmd *cobra.Command, path string) (api.MergePatch, error) {
	if err := cmd.Context().Err(); err != nil {
		return nil, managementFailure(err)
	}
	reader, err := managementInputReader(cmd, path)
	if err != nil {
		return nil, err
	}
	raw, err := readManagementInput(reader, api.MaxResourceBytes)
	closeErr := reader.Close()
	if contextErr := cmd.Context().Err(); contextErr != nil {
		clear(raw)
		return nil, managementFailure(contextErr)
	}
	if err != nil || closeErr != nil {
		clear(raw)
		return nil, &managementCommandError{"merge patch input is unreadable or exceeds 1 MiB", cpra.ErrInvalid}
	}
	trimmed := bytes.TrimSpace(raw)
	var object map[string]json.RawMessage
	if len(trimmed) == 0 || trimmed[0] != '{' || api.StrictDecode(trimmed, &object) != nil || object == nil {
		clear(raw)
		return nil, &managementCommandError{"patch must be one JSON object without duplicate fields or trailing content", cpra.ErrInvalid}
	}
	return raw, nil
}
