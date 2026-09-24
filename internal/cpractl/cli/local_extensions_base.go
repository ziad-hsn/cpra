//go:build !externaljobs

package cli

import "github.com/spf13/cobra"

func registerLocalExtensions(*cobra.Command) {}
