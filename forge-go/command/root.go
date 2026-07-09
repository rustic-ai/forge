package command

import (
	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/spf13/cobra"
)

var (
	logLevel  string
	logFormat string
	forgeHome string
)

// RootCmd represents the base command when called without any subcommands
var RootCmd = &cobra.Command{
	Use:   "forge",
	Short: "Forge is the Rustic AI agent guild orchestrator runtime",
	Long:  `Forge is a cross-platform Go runtime that handles process spawning, monitoring, and orchestration for Rustic AI agent guilds.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return RootCmd.Execute()
}

func init() {
	RootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	RootCmd.PersistentFlags().StringVar(&logFormat, "log-format", "text", "Log format (text, json)")
	RootCmd.PersistentFlags().StringVar(&forgeHome, "forge-home", "", "Base directory for Forge data (default: ~/.forge, or FORGE_HOME env)")

	RootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if forgeHome != "" {
			forgepath.SetHome(forgeHome)
		}
	}

	// Register guild command
	RootCmd.AddCommand(GuildCmd)
}
