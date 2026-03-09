package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/rustic-ai/forge/forge-go/guild"
	guildstore "github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// The contract helper executes fundamental operations on domain models via stdin/stdout
// to support cross-language contract testing from pytest.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: contract <parse-guild|parse-agent|build-guild>\n")
		os.Exit(1)
	}

	cmd := os.Args[1]
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading STDIN: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "parse-guild":
		var spec protocol.GuildSpec
		if err := json.Unmarshal(input, &spec); err != nil {
			fmt.Fprintf(os.Stderr, "Error unmarshalling GuildSpec: %v\n", err)
			os.Exit(1)
		}
		output, _ := json.Marshal(spec)
		fmt.Print(string(output))

	case "parse-agent":
		var spec protocol.AgentSpec
		if err := json.Unmarshal(input, &spec); err != nil {
			fmt.Fprintf(os.Stderr, "Error unmarshalling AgentSpec: %v\n", err)
			os.Exit(1)
		}
		output, _ := json.Marshal(spec)
		fmt.Print(string(output))

	case "build-guild":
		var spec protocol.GuildSpec
		if err := json.Unmarshal(input, &spec); err != nil {
			fmt.Fprintf(os.Stderr, "Error unmarshalling GuildSpec: %v\n", err)
			os.Exit(1)
		}

		res, err := guild.GuildBuilderFromSpec(&spec).BuildSpec()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error building GuildSpec: %v\n", err)
			os.Exit(1)
		}

		output, _ := json.Marshal(res)
		fmt.Print(string(output))

	case "metastore-write":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: contract metastore-write <sqlite_path>\n")
			os.Exit(1)
		}
		dbPath := os.Args[2]

		var spec protocol.GuildSpec
		if err := json.Unmarshal(input, &spec); err != nil {
			fmt.Fprintf(os.Stderr, "Error unmarshalling GuildSpec: %v\n", err)
			os.Exit(1)
		}

		builtSpec, err := guild.GuildBuilderFromSpec(&spec).BuildSpec()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error building GuildSpec: %v\n", err)
			os.Exit(1)
		}

		// Save to sqlite
		s, err := guildstore.NewGormStore("sqlite", dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening store: %v\n", err)
			os.Exit(1)
		}

		model := guildstore.FromGuildSpec(builtSpec, "test-org-123")

		if err := s.CreateGuild(model); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving to store: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Successfully saved guild %s to %s\n", builtSpec.ID, dbPath)

	case "generate-gemstone-ids":
		var req struct {
			Count     int `json:"count"`
			MachineID int `json:"machine_id"`
			Priority  int `json:"priority"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		gen, _ := protocol.NewGemstoneGenerator(req.MachineID)

		type result struct {
			IntID          uint64 `json:"int_id"`
			Priority       int    `json:"priority"`
			Timestamp      int64  `json:"timestamp"`
			MachineID      int    `json:"machine_id"`
			SequenceNumber int    `json:"sequence_number"`
		}

		var results []result
		for i := 0; i < req.Count; i++ {
			id, _ := gen.Generate(protocol.Priority(req.Priority))
			results = append(results, result{
				IntID:          id.ToInt(),
				Priority:       int(id.Priority),
				Timestamp:      id.Timestamp,
				MachineID:      id.MachineID,
				SequenceNumber: id.SequenceNumber,
			})
		}

		out, _ := json.Marshal(results)
		fmt.Print(string(out))

	case "parse-gemstone-ids":
		var ids []uint64
		if err := json.Unmarshal(input, &ids); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		type result struct {
			Priority       int   `json:"priority"`
			Timestamp      int64 `json:"timestamp"`
			MachineID      int   `json:"machine_id"`
			SequenceNumber int   `json:"sequence_number"`
		}

		var results []result
		for _, idInt := range ids {
			id, _ := protocol.ParseGemstoneID(idInt)
			results = append(results, result{
				Priority:       int(id.Priority),
				Timestamp:      id.Timestamp,
				MachineID:      id.MachineID,
				SequenceNumber: id.SequenceNumber,
			})
		}
		out, _ := json.Marshal(results)
		fmt.Print(string(out))

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		os.Exit(1)
	}
}
