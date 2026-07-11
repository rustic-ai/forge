package command

import (
	"strings"
	"testing"
)

func TestValidateGuild_Valid(t *testing.T) {
	path := writeSpecFile(t, "guild.json", validGuildJSON)
	var err error
	out := captureStdout(t, func() { err = validateGuild(nil, []string{path}) })
	if err != nil {
		t.Fatalf("expected valid spec, got %v", err)
	}
	if !strings.Contains(out, "PASSED") {
		t.Errorf("expected PASSED in output, got %q", out)
	}
}

func TestValidateGuild_MissingName(t *testing.T) {
	path := writeSpecFile(t, "guild.json",
		`{"name":"","agents":[{"id":"a1","name":"A","class_name":"pkg.A"}]}`)
	var err error
	out := captureStdout(t, func() { err = validateGuild(nil, []string{path}) })
	if err == nil {
		t.Fatal("expected validation error for missing name")
	}
	if !strings.Contains(out, "Guild name is required") {
		t.Errorf("expected name error, got %q", out)
	}
}

func TestValidateGuild_NoAgents(t *testing.T) {
	path := writeSpecFile(t, "guild.json", `{"name":"G","agents":[]}`)
	var err error
	out := captureStdout(t, func() { err = validateGuild(nil, []string{path}) })
	if err == nil {
		t.Fatal("expected validation error for zero agents")
	}
	if !strings.Contains(out, "at least one agent") {
		t.Errorf("expected agent-count error, got %q", out)
	}
}

func TestValidateGuild_AgentMissingClassName(t *testing.T) {
	path := writeSpecFile(t, "guild.json",
		`{"name":"G","agents":[{"id":"a1","name":"A","class_name":""}]}`)
	var err error
	out := captureStdout(t, func() { err = validateGuild(nil, []string{path}) })
	if err == nil {
		t.Fatal("expected error for missing class_name")
	}
	if !strings.Contains(out, "class_name") {
		t.Errorf("expected class_name error, got %q", out)
	}
}

func TestValidateGuild_AgentMissingIDIsWarning(t *testing.T) {
	path := writeSpecFile(t, "guild.json",
		`{"name":"G","agents":[{"id":"","name":"A","class_name":"pkg.A"}]}`)
	var err error
	out := captureStdout(t, func() { err = validateGuild(nil, []string{path}) })
	if err != nil {
		t.Fatalf("missing id should be a warning, not an error; got %v", err)
	}
	if !strings.Contains(out, "PASSED") || !strings.Contains(strings.ToLower(out), "warning") {
		t.Errorf("expected PASSED with a warning, got %q", out)
	}
}

func TestValidateGuild_BlueprintWrapper(t *testing.T) {
	path := writeSpecFile(t, "bp.json", blueprintJSON)
	var err error
	captureStdout(t, func() { err = validateGuild(nil, []string{path}) })
	if err != nil {
		t.Fatalf("expected blueprint wrapper to validate the nested spec, got %v", err)
	}
}

func TestValidateGuild_RouteWithoutAgentIsWarning(t *testing.T) {
	// A route step with neither agent nor agent_type is a warning, not an error.
	path := writeSpecFile(t, "guild.json",
		`{"name":"G","agents":[{"id":"a1","name":"A","class_name":"pkg.A"}],"routes":{"steps":[{}]}}`)
	var err error
	out := captureStdout(t, func() { err = validateGuild(nil, []string{path}) })
	if err != nil {
		t.Fatalf("route warning should not fail validation, got %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "warning") {
		t.Errorf("expected a warning about the route, got %q", out)
	}
}

func TestValidateGuild_MissingFile(t *testing.T) {
	var err error
	captureStdout(t, func() { err = validateGuild(nil, []string{"/no/such/guild.json"}) })
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidateGuild_MalformedNestedSpec(t *testing.T) {
	path := writeSpecFile(t, "bp.json", `{"name":"BP","spec":"not-an-object"}`)
	var err error
	captureStdout(t, func() { err = validateGuild(nil, []string{path}) })
	if err == nil {
		t.Fatal("expected error for malformed nested spec")
	}
}
