package command

import (
	"github.com/spf13/cobra"
)

var (
	guildBackend       string
	guildOrgID         string
	guildUserID        string
	guildUserName      string
	guildSupervisor    string
	guildPython        string
	guildVerbose       bool
	guildShowRouting   bool
	guildQuiet         bool
)

// GuildCmd is the parent command for guild-related operations
var GuildCmd = &cobra.Command{
	Use:   "guild",
	Short: "Guild testing and debugging commands",
	Long:  "Commands for running guilds locally for testing and debugging without the rustic-ui frontend",
}

var guildRunCmd = &cobra.Command{
	Use:   "run [guild-file]",
	Short: "Run a guild locally with interactive chat",
	Long: `Run a guild locally with interactive chat and message flow visualization.

This command launches a guild from a YAML/JSON spec file and provides an interactive
REPL for chatting with the guild. You can see all messages flowing between agents,
routing decisions, and transformations in real-time.

Example:
  forge guild run examples/echo.yaml
  forge guild run --verbose testdata/minimal.yaml
  forge guild run --show-routing testdata/e2e/002_basic_message_routing.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: runGuildREPL,
}

var guildInspectCmd = &cobra.Command{
	Use:   "inspect [guild-file]",
	Short: "Parse and display guild specification",
	Long: `Parse a guild spec file and display its structure.

Shows agents, routing rules, dependencies, and other guild configuration
in a human-readable format.

Example:
  forge guild inspect examples/echo.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: inspectGuild,
}

var guildValidateCmd = &cobra.Command{
	Use:   "validate [guild-file]",
	Short: "Validate guild specification",
	Long: `Validate a guild spec file for correctness.

Checks for syntax errors, missing dependencies, invalid agent references,
and other common issues.

Example:
  forge guild validate examples/echo.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: validateGuild,
}

func init() {
	GuildCmd.AddCommand(guildRunCmd)
	GuildCmd.AddCommand(guildInspectCmd)
	GuildCmd.AddCommand(guildValidateCmd)

	// Flags for 'run' command
	guildRunCmd.Flags().StringVar(&guildBackend, "backend", "nats", "Messaging backend (redis|nats)")
	guildRunCmd.Flags().StringVar(&guildOrgID, "org-id", "local-dev", "Organization ID")
	guildRunCmd.Flags().StringVar(&guildUserID, "user-id", "test-user", "User ID")
	guildRunCmd.Flags().StringVar(&guildUserName, "user-name", "Test User", "User name")
	guildRunCmd.Flags().StringVar(&guildSupervisor, "supervisor", "process", "Supervisor type (process|docker|bubblewrap)")
	guildRunCmd.Flags().StringVar(&guildPython, "python", "", "Python executable path (auto-detected if not specified)")
	guildRunCmd.Flags().BoolVarP(&guildVerbose, "verbose", "v", false, "Verbose output (show full message details)")
	guildRunCmd.Flags().BoolVarP(&guildQuiet, "quiet", "q", false, "Quiet mode (minimal startup output)")
	guildRunCmd.Flags().BoolVar(&guildShowRouting, "show-routing", true, "Show routing decisions and transformations")
}
