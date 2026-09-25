package main

import (
	"strings"
	"testing"
)

func TestLiveActivitySummarizesClaudeTools(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"},{"type":"text","text":"Running the test suite"}]}}`
	got := liveActivity("stdout", line)
	if !strings.Contains(got, "tool Bash") || !strings.Contains(got, "Running the test suite") {
		t.Fatalf("activity: %q", got)
	}
	if liveActivity("stdout", `{"type":"result","result":"private final answer"}`) != "" {
		t.Fatal("final result should stay in the saved log")
	}
}
