package command

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
		DependencyConfig: filepath.Join(forgeRoot, "conf", "dependencies.yaml"),
		AgentRegistry:    filepath.Join(forgeRoot, "conf", "forge-agent-registry.yaml"),
		ForgePythonPath:  filepath.Join(forgeRepoRoot, "forge-python"),
		SupervisorType:   guildSupervisor,
		PythonPath:       pythonPath,
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
	defer runtime.Shutdown()

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
		fmt.Printf("\n✅ Guild launched successfully!\n")
		fmt.Printf("   Guild ID: %s\n", guildID)
		fmt.Println()
	}

	// Wait a moment for agents to fully start
	time.Sleep(2 * time.Second)

	// Determine the topic to send user messages to
	// For guilds with routing rules, extract the destination from the first route
	// Otherwise use the first agent's topic (simple echo-style guilds)
	userMessageTopic := "default_topic"
	useWrappedMessages := false
	hasRoutingRules := spec.Routes != nil && len(spec.Routes.Steps) > 0

	if hasRoutingRules && len(spec.Routes.Steps) > 0 {
		// Guild has routing - check if it uses UserProxyAgent
		firstRoute := spec.Routes.Steps[0]
		if firstRoute.AgentType != nil && *firstRoute.AgentType == "rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent" {
			// This guild uses UserProxyAgent - we need to create one and send wrapped messages
			useWrappedMessages = true
			// Messages go to user:{userID} and UserProxyAgent will route them
			// according to the guild routing rules
		} else if firstRoute.Destination != nil {
			// Send directly to the route destination
			topics := firstRoute.Destination.Topics.ToSlice()
			if len(topics) > 0 {
				userMessageTopic = topics[0]
			}
		}
	} else if len(spec.Agents) > 0 && len(spec.Agents[0].AdditionalTopics) > 0 {
		// Simple guild without routing - send directly to first agent
		userMessageTopic = spec.Agents[0].AdditionalTopics[0]
	}

	// Show agent status
	if !guildQuiet {
		if err := showAgentStatus(runtime, guildID); err != nil {
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
					if strings.Contains(agentName, "test-user") || strings.Contains(agentID, "upa-") {
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
	go displayMessages(ctx, sub, runtime, spec, config.UserID, guildVerbose, guildShowRouting)

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
					showAgentStatus(runtime, guildID)
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
			if err := sendChatMessage(runtime, guildID, config.UserID, config.UserName, line, userMessageTopic, useWrappedMessages); err != nil {
				fmt.Printf("❌ Error sending message: %v\n", err)
			}
		}
	}
}

func showAgentStatus(runtime *cli.GuildRuntime, guildID string) error {
	statuses, err := runtime.GetAgentStatuses(guildID)
	if err != nil {
		return err
	}

	fmt.Println("\n🤖 Agent Status:")
	if len(statuses) == 0 {
		fmt.Println("   No agents found")
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
			fmt.Printf("   %s %s (PID: %d) - %s\n", stateIcon, agentName, status.PID, status.State)
		} else {
			fmt.Printf("   %s %s (PID: %d) - %s\n", stateIcon, agentID, status.PID, status.State)
		}
	}
	fmt.Println()

	return nil
}

func sendChatMessage(runtime *cli.GuildRuntime, guildID, userID, userName, text, topic string, useWrapped bool) error {
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

	fmt.Printf("📤 Sending to topic: %s (format: %s)\n", actualTopic, msg.Format)
	if err := runtime.PublishMessage(guildID, actualTopic, msg); err != nil {
		return fmt.Errorf("failed to publish: %w", err)
	}

	return nil
}

func displayMessages(ctx context.Context, sub *cli.GuildSubscription, runtime *cli.GuildRuntime, spec *protocol.GuildSpec, userID string, verbose, showRouting bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Messages():
			if !ok {
				return
			}
			printMessage(msg, runtime, spec, userID, verbose, showRouting)
		case err, ok := <-sub.Errors():
			if !ok {
				return
			}
			fmt.Printf("\n❌ Subscription error: %v\n> ", err)
		}
	}
}

func printMessage(msg *protocol.Message, runtime *cli.GuildRuntime, spec *protocol.GuildSpec, userID string, verbose, showRouting bool) {
	// Debug: show all message formats in verbose mode only
	if verbose {
		topicsDebug := msg.Topics.ToSlice()
		fmt.Printf("\n[DEBUG] Format: %-50s Topics: %v\n", msg.Format, topicsDebug)
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

	timestamp := time.Unix(int64(msg.Timestamp), 0).Format("15:04:05")
	topicStr := strings.Join(topics, ", ")

	// Message header
	fmt.Println("\n" + strings.Repeat("─", 70))
	fmt.Printf("📨 [%s] %s\n", timestamp, topicStr)

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
		fmt.Printf("   From: %s\n", senderName)
	} else if senderID != "" {
		// Fallback to ID if no name
		fmt.Printf("   From: %s\n", senderID)
	}

	// Message content
	if len(msg.Payload) > 0 {
		var payload map[string]interface{}
		if err := json.Unmarshal(msg.Payload, &payload); err == nil {
			// Pretty print payload
			if verbose {
				prettyPayload, _ := json.MarshalIndent(payload, "   ", "  ")
				fmt.Printf("   Payload:\n   %s\n", string(prettyPayload))
			} else {
				// Show condensed version - try different message formats
				displayed := false

				// Try chatCompletionRequest format (user messages)
				if messages, ok := payload["messages"].([]interface{}); ok && len(messages) > 0 {
					if firstMsg, ok := messages[0].(map[string]interface{}); ok {
						if content, ok := firstMsg["content"].([]interface{}); ok && len(content) > 0 {
							if textContent, ok := content[0].(map[string]interface{}); ok {
								if text, ok := textContent["text"].(string); ok {
									fmt.Printf("   💬 %s\n", text)
									displayed = true
								}
							}
						}
					}
				}

				// Try chatCompletionResponse format (agent responses)
				if !displayed {
					if choices, ok := payload["choices"].([]interface{}); ok && len(choices) > 0 {
						if choice, ok := choices[0].(map[string]interface{}); ok {
							if message, ok := choice["message"].(map[string]interface{}); ok {
								if content, ok := message["content"].(string); ok {
									fmt.Printf("   💬 %s\n", content)
									displayed = true
								}
							}
						}
					}
				}

				// Try simple text field
				if !displayed {
					if text, ok := payload["text"].(string); ok {
						fmt.Printf("   💬 %s\n", text)
						displayed = true
					}
				}

				// Try content field directly (common in some formats)
				if !displayed {
					if content, ok := payload["content"].(string); ok {
						fmt.Printf("   💬 %s\n", content)
						displayed = true
					}
				}

				// Generic payload display if nothing matched
				if !displayed {
					payloadBytes, _ := json.Marshal(payload)
					if len(payloadBytes) > 150 {
						fmt.Printf("   📄 %s...\n", string(payloadBytes[:150]))
					} else {
						fmt.Printf("   📄 %s\n", string(payloadBytes))
					}
				}
			}
		}
	}

	// Routing information
	if showRouting && len(msg.MessageHistory) > 0 {
		fmt.Println("   🔀 Routing History:")
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

			fmt.Printf("      %d. %s (%s)%s%s%s\n",
				i+1, agent, processor, fromTopic, toTopics, reasonStr)
		}
	}

	if verbose && msg.RoutingSlip != nil {
		fmt.Println("   📋 Routing Slip:")
		slipBytes, _ := json.MarshalIndent(msg.RoutingSlip, "      ", "  ")
		fmt.Printf("      %s\n", string(slipBytes))
	}

	fmt.Print("> ")
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
