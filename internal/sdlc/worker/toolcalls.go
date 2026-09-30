package worker

import (
	"bytes"
	"encoding/json"
)

// toolCallCounter counts complete structured-output lines as they arrive. This
// avoids losing calls when the retained stdout tail rolls over.
type toolCallCounter struct {
	runtime    string
	structured bool
	known      bool
	count      int64
	pending    []byte
	seen       map[string]bool
}

func newToolCallCounter(runtime string, structured bool) *toolCallCounter {
	return &toolCallCounter{runtime: runtime, structured: structured, seen: map[string]bool{}}
}

func (c *toolCallCounter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			c.pending = append(c.pending, p...)
			if len(c.pending) > 1<<20 {
				c.pending = nil
			}
			break
		}
		line := append(c.pending, p[:end]...)
		c.consume(line)
		c.pending = c.pending[:0]
		p = p[end+1:]
	}
	return n, nil
}

func (c *toolCallCounter) consume(line []byte) {
	var event struct {
		Type  string `json:"type"`
		Event string `json:"event"`
	}
	if json.Unmarshal(line, &event) != nil {
		return
	}
	if event.Type == "" {
		event.Type = event.Event
	}
	switch c.runtime {
	case "claude":
		if event.Type == "assistant" || (c.structured && event.Type == "result") {
			c.known = true
		}
	case "antigravity":
		if event.Type == "assistant" {
			c.known = true
		}
	case "codex":
		if event.Type == "item.started" || event.Type == "item.completed" || event.Type == "turn.completed" {
			c.known = true
		}
	case "cursor":
		if event.Type == "tool_call" || (c.structured && event.Type == "result") {
			c.known = true
		}
	case "opencode":
		if event.Type == "tool_use" || event.Type == "step_finish" {
			c.known = true
		}
	}
	if c.known {
		c.count += int64(countToolCalls(c.runtime, line, c.seen))
	}
}

func (c *toolCallCounter) result() *int64 {
	// A final JSON event may not end in a newline.
	if len(c.pending) > 0 {
		c.consume(c.pending)
		c.pending = nil
	}
	if !c.known {
		return nil
	}
	return &c.count
}

// countToolCalls classifies one event. IDs suppress repeated snapshots; events
// without IDs are counted individually because they cannot be matched safely.
func countToolCalls(runtime string, line []byte, seen map[string]bool) int {
	var event struct {
		Type    string `json:"type"`
		Event   string `json:"event"`
		Subtype string `json:"subtype"`
		CallID  string `json:"call_id"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"content"`
		} `json:"message"`
		Item struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"item"`
		Part struct {
			Tool string `json:"tool"`
		} `json:"part"`
	}
	if json.Unmarshal(line, &event) != nil {
		return 0
	}
	if event.Type == "" && runtime == "antigravity" {
		event.Type = event.Event
	}
	count := func(id string) int {
		if id != "" {
			key := runtime + ":" + id
			if seen[key] {
				return 0
			}
			seen[key] = true
		}
		return 1
	}
	switch runtime {
	case "claude", "antigravity":
		if event.Type != "assistant" {
			return 0
		}
		total := 0
		for _, block := range event.Message.Content {
			if block.Type == "tool_use" {
				total += count(block.ID)
			}
		}
		return total
	case "cursor":
		if event.Type == "tool_call" && event.Subtype == "started" {
			return count(event.CallID)
		}
	case "codex":
		if event.Type == "item.completed" {
			switch event.Item.Type {
			case "command_execution", "file_change", "mcp_tool_call", "web_search":
				return count(event.Item.ID)
			}
		}
	case "opencode":
		if event.Type == "tool_use" && event.Part.Tool != "" {
			return 1
		}
	}
	return 0
}
