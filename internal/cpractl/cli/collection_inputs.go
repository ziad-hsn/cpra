package cli

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func addCollectionInputFlags(cmd *cobra.Command, filenames *[]string, settings *collection.Options) {
	cmd.Flags().StringArrayVarP(filenames, "filename", "f", nil, "YAML/JSON file, directory, - for stdin, or explicitly selected URL; repeat for multiple inputs")
	cmd.Flags().BoolVarP(&settings.Recursive, "recursive", "R", false, "include YAML/JSON files in nested directories")
	cmd.Flags().BoolVar(&settings.AllowHTTP, "allow-http-sources", false, "explicitly allow unauthenticated HTTP input URLs (separate from API transport)")
	cmd.Flags().Int64Var(&settings.MaxSourceBytes, "max-source-bytes", 0, "cumulative source byte quota (0 uses the staging quota)")
	cmd.Flags().Int64Var(&settings.MaxStagingBytes, "max-staging-bytes", 0, "private temporary staging byte quota (0 uses the SDK default of 1 GiB)")
}

func freezeCollectionInputs(cmd *cobra.Command, filenames []string, settings collection.Options) (*collection.Frozen, error) {
	return freezeCollectionInputsProfile(cmd, filenames, settings, "")
}

func freezeCollectionInputsProfile(cmd *cobra.Command, filenames []string, settings collection.Options, profile string) (*collection.Frozen, error) {
	if profile != "" && profile != collection.FileNormalizationProfile {
		return nil, errors.New("unsupported file profile; use cpra.file.base.v1 or omit --file-profile")
	}
	sources, cleanup, err := collectionInputSources(cmd, filenames, true)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var frozen *collection.Frozen
	if profile == "" {
		frozen, err = collection.Freeze(cmd.Context(), sources, settings)
	} else {
		frozen, err = collection.FreezeProfile(cmd.Context(), sources, profile, settings)
	}
	if err != nil {
		if cmd.Context().Err() != nil {
			return nil, managementFailure(cmd.Context().Err())
		}
		return nil, &managementCommandError{"collection inputs could not be frozen; check input syntax, identities, permissions and byte limits", err}
	}
	return frozen, nil
}

// collectionInputSources selects sources without opening files or fetching URLs.
// The caller holds cleanup through all SDK source reads. Only process-owned stdin
// is closed on cancellation; injected readers retain their caller's lifecycle.
func collectionInputSources(cmd *cobra.Command, filenames []string, required bool) (_ []collection.Source, cleanup func(), err error) {
	cleanup = func() {}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	if required && len(filenames) == 0 {
		return nil, cleanup, errors.New("select at least one input with -f; use -f - for stdin")
	}
	sources := make([]collection.Source, 0, len(filenames))
	stdin := false
	for _, name := range filenames {
		switch {
		case name == "":
			return nil, cleanup, errors.New("input filenames must not be empty")
		case name == "-":
			if stdin {
				return nil, cleanup, errors.New("stdin may be selected only once")
			}
			stdin = true
			reader := cmd.InOrStdin()
			if reader == os.Stdin {
				stopClosing := context.AfterFunc(cmd.Context(), func() { _ = os.Stdin.Close() })
				cleanup = func() { stopClosing() }
				reader = diffStdinReader{ctx: cmd.Context(), reader: os.Stdin}
			}
			sources = append(sources, collection.Reader("stdin", reader))
		case strings.HasPrefix(strings.ToLower(name), "https://") || strings.HasPrefix(strings.ToLower(name), "http://"):
			sources = append(sources, collection.URL(name))
		case strings.Contains(name, "://"):
			return nil, cleanup, errors.New("remote inputs must use HTTPS; HTTP requires --allow-http-sources")
		default:
			sources = append(sources, collection.File(name))
		}
	}
	return sources, cleanup, nil
}
