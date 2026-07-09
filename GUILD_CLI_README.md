# Forge Guild CLI

A command-line interface for running and debugging Forge guilds locally without the rustic-ui frontend.

## Features

- 🚀 Launch guilds from JSON/YAML specs
- 💬 Interactive REPL for chatting with guilds  
- 📊 Real-time message flow visualization
- 🔍 Agent status monitoring
- 🔀 Routing decision display
- ⚡ Ctrl+C signal handling for clean shutdown
- 🐍 Auto-detection of Python 3.13+
- 🔕 Quiet mode to hide noisy logs
- ✅ Guild spec validation and inspection

## Prerequisites

- Python 3.13+ (auto-detected from pyenv or system)
- Go 1.21+ (for building)
- Redis or NATS backend

## Installation

```bash
cd /home/nihal/Projects/forge/forge-go
go build -o forge .
# Optionally install to PATH
sudo cp forge /usr/local/bin/
```

## Quick Start

```bash
# Run a guild (quiet mode)
./forge guild run -q ../guilds/echo_app.json

# Type messages to interact with the guild
> hello!

# Use commands
> /status
> /help
> /quit
```

## Usage

### Run a Guild

```bash
# Basic usage
./forge guild run ../guilds/echo_app.json

# With custom Python
./forge guild run --python /path/to/python3.13 ../guilds/echo_app.json

# Quiet mode (minimal startup output, clean display)
./forge guild run -q ../guilds/echo_app.json

# Verbose mode (show all message details)
./forge guild run -v ../guilds/echo_app.json

# Show routing information
./forge guild run --show-routing ../guilds/echo_app.json
```

### Inspect a Guild Spec

```bash
./forge guild inspect ../guilds/echo_app.json
```

Shows guild structure, agents, routing rules, and dependencies.

### Validate a Guild Spec

```bash
./forge guild validate ../guilds/echo_app.json
```

Checks for syntax errors and validates configuration.

## Interactive Commands

Once the REPL starts, you can use:

- Type any text to send a chat message to the guild
- `/status` - Show current agent status
- `/help` - Show help message
- `/quit` or `/exit` - Exit the REPL
- `Ctrl+C` - Shutdown cleanly

## Flags

### Common Flags

- `--backend` - Messaging backend: `redis` or `nats` (default: `nats`)
- `--org-id` - Organization ID (default: `local-dev`)
- `--user-id` - User ID for sending messages (default: `test-user`)
- `--user-name` - User display name (default: `Test User`)
- `--supervisor` - Supervisor type: `process`, `docker`, or `bubblewrap` (default: `process`)
- `--python` - Python executable path (auto-detected if not specified)

### Output Control

- `-q, --quiet` - Minimal startup output, hide noisy logs (recommended)
- `-v, --verbose` - Show full message details including payloads
- `--show-routing` - Show routing history and transformations (default: `true`)

## Message Display

The CLI automatically filters noisy internal messages:

**Shown by default:**
- ✅ User chat messages
- ✅ Agent responses  
- ✅ Errors and warnings
- ✅ Important state changes

**Hidden by default** (use `-v` to see):
- ❌ Health checks and heartbeats
- ❌ Internal state updates
- ❌ Infrastructure events
- ❌ HTTP request logs

## Python Version

The CLI requires **Python 3.13+**. It auto-detects in this order:

1. `pyenv which python` (preferred - gets real path, not shim)
2. `python` from PATH
3. `python3` from PATH

### Setting up Python 3.13

If you're getting Python version errors, recreate your virtual environment:

```bash
cd /home/nihal/Projects/forge

# Remove old venv
rm -rf .env

# Create new venv with Python 3.13
python3.13 -m venv .env
# OR if pyenv is active with 3.13:
python -m venv .env

# Activate and install
source .env/bin/activate
pip install -e ./forge-python
```

## Troubleshooting

### "Python 3.12 does not satisfy Python>=3.13"

Your virtual environment was created with Python 3.12. See "Setting up Python 3.13" above.

### "could not find forge root"

Run the CLI from the `forge-go` directory or any subdirectory of the forge repository.

### Server logs cluttering output

Use `-q` flag for quiet mode. Server logs are automatically redirected to `/tmp/forge-cli-*/server.log`.

### Guild won't launch

1. Check Python version at startup (should show 3.13+)
2. Use `/status` to check if agents are running
3. Look at server logs: `/tmp/forge-cli-*/server.log`
4. Verify agent registry seeded: Look for "Seeding agent registry" at startup

### Ctrl+C doesn't work

This was fixed. Update to latest version and rebuild.

## Examples

### Echo Guild (recommended for testing)

```bash
./forge guild run -q ../guilds/echo_app.json
```

Type messages and see them echoed back by the agent.

### React Guild

```bash
./forge guild run ../guilds/react_guild.json
```

### Custom Configuration

```bash
./forge guild run \
  --backend redis \
  --org-id my-org \
  --user-id alice \
  --user-name "Alice Smith" \
  --python /home/nihal/.pyenv/versions/3.13.0/bin/python \
  -q \
  ../guilds/echo_app.json
```

## Development

### Project Structure

```
forge-go/
├── cli/
│   ├── guild_runtime.go      # Embedded runtime
│   ├── subscription.go        # Message subscriptions
│   └── message_builder.go     # Message construction
├── command/
│   ├── guild.go              # Command group
│   ├── guild_run.go          # Interactive REPL
│   ├── guild_inspect.go      # Guild inspection
│   └── guild_validate.go     # Guild validation
└── go.mod                    # Dependencies
```

### Architecture

```
┌─────────────────────────────────────────┐
│  CLI REPL (guild_run.go)               │
│  - User input handling                  │
│  - Message display                      │
│  - Command processing                   │
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│  GuildRuntime (guild_runtime.go)       │
│  - Embedded forge server                │
│  - Agent registry seeding               │
│  - Guild lifecycle management           │
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│  Forge Server (embedded)                │
│  - Redis/NATS messaging                 │
│  - Agent supervision                    │
│  - Guild management API                 │
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│  Python Agents (Python 3.13+)          │
│  - Guild manager agent                  │
│  - User-defined agents                  │
│  - Message processing                   │
└─────────────────────────────────────────┘
```

## License

Same as the Forge project.
