package command

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rustic-ai/forge/forge-go/cli"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/spf13/cobra"
)

// guildRuntime is the subset of *cli.GuildRuntime that the display/publish
// helpers depend on. Defining it here lets the helpers be unit-tested with a
// fake; *cli.GuildRuntime satisfies it.
type guildRuntime interface {
	GetAgentStatuses(guildID string) (map[string]cli.AgentStatus, error)
	GetAgentName(agentID string) string
	PublishMessage(namespace, topic string, msg *protocol.Message) error
}

// messageSource is the subset of *cli.GuildSubscription that displayMessages
// consumes; *cli.GuildSubscription satisfies it.
type messageSource interface {
	Messages() <-chan *protocol.Message
	Errors() <-chan error
}

func runGuildREPL(cmd *cobra.Command, args []string) error {
	guildFile := args[0]

	// Get current working directory and forge root
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Find forge root (look for go.mod)
	forgeRoot := findForgeRoot(cwd)
	if forgeRoot == "" {
		return fmt.Errorf("could not find forge root (no go.mod found)")
	}

	// Auto-detect Python if not specified
	pythonPath := guildPython
	if pythonPath == "" {
		pythonPath = detectPython()
	}

	// Set up runtime config
	// forgeRoot is already the forge-go directory
	forgeRepoRoot := filepath.Dir(forgeRoot) // Parent is the repo root
	config := cli.RuntimeConfig{
		Backend:          guildBackend,
		OrgID:            guildOrgID,
		UserID:           guildUserID,
		UserName:         guildUserName,
		ForgeRoot:        forgeRepoRoot,
		DependencyConfig: filepath.Join(forgeRoot, "conf", "agent-dependencies.yaml"),
		AgentRegistry:    filepath.Join(forgeRoot, "conf", "forge-agent-registry.yaml"),
		ForgePythonPath:  filepath.Join(forgeRepoRoot, "forge-python"),
		SupervisorType:   guildSupervisor,
		PythonPath:       pythonPath,
		UVPython:         guildUVPython,
	}

	if !guildQuiet {
		if pythonPath != "" {
			fmt.Printf("   Python: %s\n", pythonPath)
		}

		fmt.Println("🚀 Starting Forge Guild CLI...")
		fmt.Printf("   Backend: %s\n", config.Backend)
		fmt.Printf("   Guild: %s\n", guildFile)
		fmt.Println()
	}

	// Create and start runtime
	runtime, err := cli.NewGuildRuntime(config)
	if err != nil {
		return fmt.Errorf("failed to create runtime: %w", err)
	}
	defer func() { _ = runtime.Shutdown() }()

	if !guildQuiet {
		fmt.Println("⚙️  Starting embedded forge server...")
	}
	if err := runtime.Start(); err != nil {
		return fmt.Errorf("failed to start runtime: %w", err)
	}

	// Load guild spec
	if !guildQuiet {
		fmt.Printf("📋 Loading guild spec from %s...\n", guildFile)
	}
	spec, err := runtime.LoadGuild(guildFile)
	if err != nil {
		return fmt.Errorf("failed to load guild: %w", err)
	}

	// Launch guild
	if !guildQuiet {
		fmt.Printf("🎯 Launching guild '%s'...\n", spec.Name)
	}
	guildID, err := runtime.LaunchGuild(spec)
	if err != nil {
		return fmt.Errorf("failed to launch guild: %w", err)
	}

	if !guildQuiet {
		fmt.Print("\n✅ Guild launched successfully!\n")
		fmt.Printf("   Guild ID: %s\n", guildID)
		fmt.Println()
	}

	// Wait for the guild manager to finish initializing the guild before we
	// create the UserProxyAgent or send any messages. Until the guild is
	// initialized the manager rejects a UserProxyAgent creation request with
	// "Guild is not initialized", and agent-process liveness is not a reliable
	// signal (it is set at spawn, before initialization). Mirror rustic-ui and
	// wait for the guild to report ready.
	if !guildQuiet {
		fmt.Println("⏳ Waiting for guild to initialize...")
	}
	if err := runtime.WaitForGuildReady(guildID, 2*time.Minute); err != nil {
		fmt.Printf("⚠️  Warning: guild did not report ready: %v (continuing anyway)\n", err)
	}

	// Determine the topic to send user messages to.
	userMessageTopic, useWrappedMessages := selectUserMessageTopic(spec)

	// Show agent status
	if !guildQuiet {
		if err := showAgentStatus(os.Stdout, runtime, guildID); err != nil {
			fmt.Printf("⚠️  Warning: could not get agent status: %v\n", err)
		}

		fmt.Println("\n📡 Subscribing to message topics...")
	}

	sub, err := runtime.Subscribe(guildID, config.UserID, spec)
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}
	defer sub.Close()

	// Create UserProxyAgent only if this guild uses wrapped messages
	if useWrappedMessages {
		if !guildQuiet {
			fmt.Println("   Creating UserProxyAgent...")
		}
		creationMsg, err := cli.BuildUserProxyCreationRequest(config.UserID, config.UserName)
		if err != nil {
			return fmt.Errorf("failed to build UserProxyAgent creation request: %w", err)
		}
		if err := runtime.PublishMessage(guildID, "system_topic", creationMsg); err != nil {
			return fmt.Errorf("failed to create UserProxyAgent: %w", err)
		}

		// Wait for UserProxyAgent to be created and appear in agent status
		if !guildQuiet {
			fmt.Println("   Waiting for UserProxyAgent to start...")
		}
		userProxyCreated := false
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			statuses, err := runtime.GetAgentStatuses(guildID)
			if err == nil {
				for agentID := range statuses {
					agentName := runtime.GetAgentName(agentID)
					if strings.Contains(agentName, config.UserID) || strings.Contains(agentID, "upa-") {
						userProxyCreated = true
						if guildVerbose {
							fmt.Printf("   UserProxyAgent created: %s\n", agentName)
						}
						break
					}
				}
			}
			if userProxyCreated {
				break
			}
		}
		if !userProxyCreated && guildVerbose {
			fmt.Println("   Warning: UserProxyAgent may not have been created")
		}
	}

	if guildVerbose {
		if useWrappedMessages {
			fmt.Printf("   Sending wrapped messages to: user:%s (routes to %s)\n\n", config.UserID, userMessageTopic)
		} else {
			fmt.Printf("   Sending user messages to: %s\n\n", userMessageTopic)
		}
	}

	// Set up context for shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start message display goroutine
	go displayMessages(ctx, os.Stdout, sub, runtime, config.UserID, guildVerbose, guildShowRouting)

	// Interactive REPL
	fmt.Println("\n" + strings.Repeat("=", 70))
	fmt.Println("💬 Interactive Chat - Type your messages below")
	fmt.Println("   Commands:")
	fmt.Println("     /quit or /exit - Exit the REPL")
	fmt.Println("     /status - Show agent status")
	fmt.Println("     /help - Show this help")
	fmt.Println("     Ctrl+C - Shutdown")
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println()

	// Create channel for stdin input
	inputChan := make(chan string)
	errChan := make(chan error)

	// Start goroutine to read from stdin
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			case inputChan <- scanner.Text():
			}
		}
		if err := scanner.Err(); err != nil {
			errChan <- err
		}
		close(inputChan)
	}()

	// Main REPL loop
	for {
		fmt.Print("> ")

		select {
		case <-ctx.Done():
			fmt.Println("\n👋 Shutting down...")
			return nil

		case err := <-errChan:
			return fmt.Errorf("error reading input: %w", err)

		case line, ok := <-inputChan:
			if !ok {
				// Input channel closed (EOF)
				fmt.Println("\n👋 Shutting down...")
				return nil
			}

			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			// Handle commands
			if strings.HasPrefix(line, "/") {
				switch strings.ToLower(line) {
				case "/quit", "/exit":
					fmt.Println("👋 Goodbye!")
					return nil
				case "/status":
					if err := showAgentStatus(os.Stdout, runtime, guildID); err != nil {
						fmt.Printf("⚠️  Warning: could not get agent status: %v\n", err)
					}
					continue
				case "/help":
					fmt.Println("\nCommands:")
					fmt.Println("  /quit, /exit - Exit the REPL")
					fmt.Println("  /status - Show agent status")
					fmt.Println("  /help - Show this help")
					fmt.Println("  Ctrl+C - Shutdown")
					fmt.Println()
					continue
				default:
					fmt.Printf("Unknown command: %s\n", line)
					continue
				}
			}

			// Send chat message
			if err := sendChatMessage(os.Stdout, runtime, guildID, config.UserID, config.UserName, line, userMessageTopic, useWrappedMessages); err != nil {
				fmt.Printf("❌ Error sending message: %v\n", err)
			}
		}
	}
}

func showAgentStatus(w io.Writer, runtime guildRuntime, guildID string) error {
	statuses, err := runtime.GetAgentStatuses(guildID)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "\n🤖 Agent Status:")
	if len(statuses) == 0 {
		fmt.Fprintln(w, "   No agents found")
		return nil
	}

	for agentID, status := range statuses {
		stateIcon := "●"
		switch status.State {
		case "running":
			stateIcon = "🟢"
		case "starting":
			stateIcon = "🟡"
		case "stopped":
			stateIcon = "⚫"
		case "error":
			stateIcon = "🔴"
		}

		// Get agent display name
		agentName := runtime.GetAgentName(agentID)
		if agentName != agentID {
			fmt.Fprintf(w, "   %s %s (PID: %d) - %s\n", stateIcon, agentName, status.PID, status.State)
		} else {
			fmt.Fprintf(w, "   %s %s (PID: %d) - %s\n", stateIcon, agentID, status.PID, status.State)
		}
	}
	fmt.Fprintln(w)

	return nil
}

// selectUserMessageTopic decides which topic user input should be published to
// and whether messages must be wrapped for a UserProxyAgent. Guilds with a
// UserProxyAgent first route use wrapped messages; otherwise the first route's
// destination topic (or, with no routing, the first agent's additional topic) is
// used, falling back to "default_topic".
func selectUserMessageTopic(spec *protocol.GuildSpec) (topic string, useWrapped bool) {
	topic = "default_topic"

	if spec.Routes != nil && len(spec.Routes.Steps) > 0 {
		firstRoute := spec.Routes.Steps[0]
		if firstRoute.AgentType != nil && *firstRoute.AgentType == "rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent" {
			// Messages go to user:{userID} and the UserProxyAgent routes them.
			return topic, true
		}
		if firstRoute.Destination != nil {
			if topics := firstRoute.Destination.Topics.ToSlice(); len(topics) > 0 {
				return topics[0], false
			}
		}
		return topic, false
	}

	if len(spec.Agents) > 0 && len(spec.Agents[0].AdditionalTopics) > 0 {
		return spec.Agents[0].AdditionalTopics[0], false
	}

	return topic, false
}

func sendChatMessage(w io.Writer, runtime guildRuntime, guildID, userID, userName, text, topic string, useWrapped bool) error {
	var msg *protocol.Message
	var err error
	var actualTopic string

	if useWrapped {
		// Build a wrapped message for UserProxyAgent
		msg, err = cli.BuildWrappedChatMessage(userID, userName, text)
		if err != nil {
			return err
		}
		// Extract the actual topic from the message (should be user:{userID})
		actualTopic = msg.Topics.ToSlice()[0]
	} else {
		// Build a regular chat message
		msg, err = cli.BuildChatMessage(userID, userName, text, topic)
		if err != nil {
			return err
		}
		actualTopic = topic
	}

	fmt.Fprintf(w, "📤 Sending to topic: %s (format: %s)\n", actualTopic, msg.Format)
	if err := runtime.PublishMessage(guildID, actualTopic, msg); err != nil {
		return fmt.Errorf("failed to publish: %w", err)
	}

	return nil
}

func displayMessages(ctx context.Context, w io.Writer, sub messageSource, runtime guildRuntime, userID string, verbose, showRouting bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Messages():
			if !ok {
				return
			}
			printMessage(w, msg, runtime, userID, verbose, showRouting)
		case err, ok := <-sub.Errors():
			if !ok {
				return
			}
			fmt.Fprintf(w, "\n❌ Subscription error: %v\n> ", err)
		}
	}
}

func printMessage(w io.Writer, msg *protocol.Message, runtime guildRuntime, userID string, verbose, showRouting bool) {
	// Debug: show all message formats in verbose mode only
	if verbose {
		topicsDebug := msg.Topics.ToSlice()
		fmt.Fprintf(w, "\n[DEBUG] Format: %-50s Topics: %v\n", msg.Format, topicsDebug)
	}

	// Skip internal system messages unless verbose
	if !verbose {
		// Skip health/heartbeat messages
		if msg.Format == "healthReport" || msg.Format == "heartbeat" {
			return
		}
		// Skip agent health reports
		if msg.Format == "rustic_ai.core.guild.agent_ext.mixins.health.AgentsHealthReport" {
			return
		}
		// Skip guild updated announcements (noisy during startup)
		if msg.Format == "rustic_ai.core.agents.system.models.GuildUpdatedAnnouncement" {
			return
		}
		// Skip state fetch responses
		if msg.Format == "rustic_ai.core.state.models.StateFetchResponse" {
			return
		}
		// Skip infra events unless they're errors
		if msg.Format == "rustic_ai.forge.runtime.InfraEvent" {
			return
		}
		// Skip participant list updates (noisy)
		if msg.Format == "rustic_ai.core.agents.utils.user_proxy_agent.ParticipantList" {
			return
		}
	}

	topics := msg.Topics.ToSlice()

	if !verbose {
		// Skip messages on user:{userID} topic - these are echoes of our sent messages
		if len(topics) > 0 && topics[0] == "user:"+userID {
			return
		}

		// For user_notifications: show only agent responses (3+ routing entries), skip unwrap notifications
		if len(topics) > 0 && topics[0] == "user_notifications:"+userID && len(msg.MessageHistory) < 3 {
			return
		}
	}

	// msg.Timestamp is milliseconds since the Unix epoch (derived from the
	// GemstoneID clock, which uses UnixMilli), so decode it as milliseconds.
	timestamp := time.UnixMilli(int64(msg.Timestamp)).Format("15:04:05")
	topicStr := strings.Join(topics, ", ")

	// Message header
	fmt.Fprintln(w, "\n"+strings.Repeat("─", 70))
	fmt.Fprintf(w, "📨 [%s] %s\n", timestamp, topicStr)

	// Get sender name - use agent name map if available
	senderName := ""
	senderID := ""
	if msg.Sender.Name != nil && *msg.Sender.Name != "" {
		senderName = *msg.Sender.Name
	}
	if msg.Sender.ID != nil {
		senderID = *msg.Sender.ID
		// Try to get a better name from the agent map
		if agentName := runtime.GetAgentName(senderID); agentName != senderID {
			senderName = agentName
		}
	}

	// Display sender - just the name, not the ID (cleaner)
	if senderName != "" {
		fmt.Fprintf(w, "   From: %s\n", senderName)
	} else if senderID != "" {
		// Fallback to ID if no name
		fmt.Fprintf(w, "   From: %s\n", senderID)
	}

	// Message content
	if len(msg.Payload) > 0 {
		var payload map[string]any
		if err := json.Unmarshal(msg.Payload, &payload); err == nil {
			// Pretty print payload
			if verbose {
				prettyPayload, _ := json.MarshalIndent(payload, "   ", "  ")
				fmt.Fprintf(w, "   Payload:\n   %s\n", string(prettyPayload))
			} else {
				// Show condensed version - try different message formats
				displayed := false

				// Try chatCompletionRequest format (user messages)
				if messages, ok := payload["messages"].([]any); ok && len(messages) > 0 {
					if firstMsg, ok := messages[0].(map[string]any); ok {
						if content, ok := firstMsg["content"].([]any); ok && len(content) > 0 {
							if textContent, ok := content[0].(map[string]any); ok {
								if text, ok := textContent["text"].(string); ok {
									fmt.Fprintf(w, "   💬 %s\n", text)
									displayed = true
								}
							}
						}
					}
				}

				// Try chatCompletionResponse format (agent responses)
				if !displayed {
					if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
						if choice, ok := choices[0].(map[string]any); ok {
							if message, ok := choice["message"].(map[string]any); ok {
								if content, ok := message["content"].(string); ok {
									fmt.Fprintf(w, "   💬 %s\n", content)
									displayed = true
								}
							}
						}
					}
				}

				// Try simple text field
				if !displayed {
					if text, ok := payload["text"].(string); ok {
						fmt.Fprintf(w, "   💬 %s\n", text)
						displayed = true
					}
				}

				// Try content field directly (common in some formats)
				if !displayed {
					if content, ok := payload["content"].(string); ok {
						fmt.Fprintf(w, "   💬 %s\n", content)
						displayed = true
					}
				}

				// Generic payload display if nothing matched
				if !displayed {
					payloadBytes, _ := json.Marshal(payload)
					if len(payloadBytes) > 150 {
						fmt.Fprintf(w, "   📄 %s...\n", string(payloadBytes[:150]))
					} else {
						fmt.Fprintf(w, "   📄 %s\n", string(payloadBytes))
					}
				}
			}
		}
	}

	// Routing information
	if showRouting && len(msg.MessageHistory) > 0 {
		fmt.Fprintln(w, "   🔀 Routing History:")
		for i, entry := range msg.MessageHistory {
			agent := ""
			agentID := ""
			if entry.Agent.Name != nil && *entry.Agent.Name != "" {
				agent = *entry.Agent.Name
			}
			if entry.Agent.ID != nil {
				agentID = *entry.Agent.ID
				// Try to get better name from runtime
				if betterName := runtime.GetAgentName(agentID); betterName != agentID {
					agent = betterName
				} else if agent == "" {
					agent = agentID
				}
			}
			if agent == "" {
				agent = "unknown"
			}
			processor := entry.Processor
			if processor == "" {
				processor = "unknown"
			}

			reasonStr := ""
			if len(entry.Reason) > 0 {
				reasonStr = fmt.Sprintf(" [%s]", strings.Join(entry.Reason, ", "))
			}

			fromTopic := ""
			if entry.FromTopic != nil {
				fromTopic = fmt.Sprintf(" from %s", *entry.FromTopic)
			}

			toTopics := ""
			if len(entry.ToTopics) > 0 {
				toTopics = fmt.Sprintf(" → %s", strings.Join(entry.ToTopics, ", "))
			}

			fmt.Fprintf(w, "      %d. %s (%s)%s%s%s\n",
				i+1, agent, processor, fromTopic, toTopics, reasonStr)
		}
	}

	if verbose && msg.RoutingSlip != nil {
		fmt.Fprintln(w, "   📋 Routing Slip:")
		slipBytes, _ := json.MarshalIndent(msg.RoutingSlip, "      ", "  ")
		fmt.Fprintf(w, "      %s\n", string(slipBytes))
	}

	fmt.Fprint(w, "> ")
}

func findForgeRoot(startDir string) string {
	// First try current directory and walk up
	dir := startDir
	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			// Check if this is the forge-go module
			content, _ := os.ReadFile(goModPath)
			if strings.Contains(string(content), "github.com/rustic-ai/forge/forge-go") {
				// Return the forge-go directory itself
				return dir
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// If not found walking up, try common subdirectory patterns
	// This handles the case where we're in the repo root but go.mod is in forge-go/
	commonSubdirs := []string{"forge-go", "go", "backend"}
	for _, subdir := range commonSubdirs {
		forgeGoPath := filepath.Join(startDir, subdir)
		goModPath := filepath.Join(forgeGoPath, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			content, _ := os.ReadFile(goModPath)
			if strings.Contains(string(content), "github.com/rustic-ai/forge/forge-go") {
				return forgeGoPath
			}
		}
	}

	return ""
}

func detectPython() string {
	// Try pyenv which command first to get the real Python path (not shim)
	cmd := exec.Command("pyenv", "which", "python")
	if output, err := cmd.Output(); err == nil {
		realPath := strings.TrimSpace(string(output))
		// Verify it's Python 3.13+
		versionCmd := exec.Command(realPath, "--version")
		if versionOutput, err := versionCmd.Output(); err == nil {
			version := strings.TrimSpace(string(versionOutput))
			if strings.Contains(version, "Python 3.13") || strings.Contains(version, "Python 3.14") {
				return realPath
			}
		}
	}

	// Try python from PATH (may be a shim)
	if pythonPath, err := exec.LookPath("python"); err == nil {
		cmd := exec.Command(pythonPath, "--version")
		if output, err := cmd.Output(); err == nil {
			version := strings.TrimSpace(string(output))
			if strings.Contains(version, "Python 3.13") || strings.Contains(version, "Python 3.14") {
				return pythonPath
			}
		}
	}

	// Try python3 as fallback
	if pythonPath, err := exec.LookPath("python3"); err == nil {
		cmd := exec.Command(pythonPath, "--version")
		if output, err := cmd.Output(); err == nil {
			version := strings.TrimSpace(string(output))
			if strings.Contains(version, "Python 3.13") || strings.Contains(version, "Python 3.14") {
				return pythonPath
			}
		}
		// Return python3 even if not 3.13, better than nothing
		return pythonPath
	}

	return ""
}
