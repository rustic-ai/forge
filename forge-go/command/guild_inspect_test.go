package command

import (
	"strings"
	"testing"
)

func TestInspectGuild_Valid(t *testing.T) {
	path := writeSpecFile(t, "guild.json", validGuildJSON)
	var err error
	out := captureStdout(t, func() { err = inspectGuild(nil, []string{path}) })
	if err != nil {
		t.Fatalf("inspectGuild: %v", err)
	}
	// The guild name and the agents section should be printed.
	if !strings.Contains(out, "Echo") {
		t.Errorf("expected guild name in output, got %q", out)
	}
	if !strings.Contains(out, "Agents (1)") {
		t.Errorf("expected agents section, got %q", out)
	}
	if !strings.Contains(out, "EchoAgent") {
		t.Errorf("expected agent name in output, got %q", out)
	}
}

func TestInspectGuild_BlueprintWrapperUnwrapped(t *testing.T) {
	path := writeSpecFile(t, "bp.json", blueprintJSON)
	var err error
	out := captureStdout(t, func() { err = inspectGuild(nil, []string{path}) })
	if err != nil {
		t.Fatalf("inspectGuild: %v", err)
	}
	if !strings.Contains(out, "Inner") {
		t.Errorf("expected unwrapped nested guild name 'Inner', got %q", out)
	}
}

// richGuildJSON exercises every section inspectGuild prints: description, id,
// agent description/topics, routing rule (agent, method, origin filter,
// destination topics + recipients, transformer), dependencies, and properties.
const richGuildJSON = `{
  "name":"Rich","description":"a rich guild","id":"guild-42",
  "agents":[{"id":"a1","name":"AgentOne","class_name":"pkg.One","description":"the one","additional_topics":["topicX"]}],
  "routes":{"steps":[{
    "agent":{"name":"AgentOne"},
    "method_name":"handle",
    "origin_filter":{"origin_message_format":"fmtA","origin_topic":"topicO"},
    "destination":{"topics":["destT"],"recipient_list":[{"id":"r1"}]},
    "transformer":{"expression":"$"}
  }]},
  "dependency_map":{"llm":{"class_name":"pkg.LLM"}},
  "properties":{"k":"v"}
}`

func TestInspectGuild_AllSections(t *testing.T) {
	path := writeSpecFile(t, "rich.json", richGuildJSON)
	var err error
	out := captureStdout(t, func() { err = inspectGuild(nil, []string{path}) })
	if err != nil {
		t.Fatalf("inspectGuild: %v", err)
	}
	for _, want := range []string{
		"Description: a rich guild", "ID: guild-42",
		"Additional Topics", "Routing Rules (1)", "Method: handle",
		"Origin Format: fmtA", "Origin Topic: topicO",
		"Destination Topics", "Recipients: 1 agents", "Transformer: present",
		"Dependencies (1)", "Properties:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q\n---\n%s", want, out)
		}
	}
}

func TestInspectGuild_MissingFile(t *testing.T) {
	var err error
	captureStdout(t, func() { err = inspectGuild(nil, []string{"/no/such/guild.json"}) })
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestInspectGuild_UnsupportedExtension(t *testing.T) {
	path := writeSpecFile(t, "guild.txt", "not a guild")
	var err error
	captureStdout(t, func() { err = inspectGuild(nil, []string{path}) })
	if err == nil {
		t.Fatal("expected error for unsupported extension")
	}
}
