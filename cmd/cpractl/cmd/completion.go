package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

func init() {
	completionCmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for cpractl.

To load completions:

Bash:
  $ source <(cpractl completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ cpractl completion bash > /etc/bash_completion.d/cpractl
  # macOS:
  $ cpractl completion bash > $(brew --prefix)/etc/bash_completion.d/cpractl

Zsh:
  $ source <(cpractl completion zsh)

  # To load completions for each session, execute once:
  $ cpractl completion zsh > "${fpath[1]}/_cpractl"

Fish:
  $ cpractl completion fish | source

  # To load completions for each session, execute once:
  $ cpractl completion fish > ~/.config/fish/completions/cpractl.fish

PowerShell:
  PS> cpractl completion powershell | Out-String | Invoke-Expression
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return rootCmd.GenBashCompletion(os.Stdout)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				return rootCmd.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
			}
			return nil
		},
	}
	rootCmd.AddCommand(completionCmd)
}
