package main

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/OWNER/jevkit/internal/redact/config"
)

// sdlcLiveOutput presents bounded runtime activity while the saved log keeps
// the complete stream. Redaction happens before a byte reaches the terminal.
func (a *App) sdlcLiveOutput(agent string) (func(string, string), error) {
	cfg, err := config.Load(a.loadOptions())
	if err != nil {
		return nil, err
	}
	r, err := cfg.Redactor()
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	return func(stream, line string) {
		label := liveActivity(stream, line)
		if label == "" {
			return
		}
		clean, err := r.Apply(label)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		a.outf("    %s: %s\n", agent, shortProgressText(clean.Text, 240))
	}, nil
}

func liveActivity(stream, line string) string {
	if stream == "stderr" {
		return "stderr: " + strings.TrimSpace(line)
	}
	var event struct {
		Type  string `json:"type"`
		Event string `json:"event"`
		Item  struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"item"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return strings.TrimSpace(line)
	}
	if event.Type == "assistant" {
		var parts []string
		for _, block := range event.Message.Content {
			if block.Type == "tool_use" {
				parts = append(parts, "tool "+block.Name)
			} else if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				parts = append(parts, "assistant: "+block.Text)
			}
		}
		return strings.Join(parts, "; ")
	}
	if event.Type == "item.started" || event.Type == "item.completed" {
		return event.Type + ": " + event.Item.Type + " " + event.Item.Name
	}
	if event.Event != "" && event.Event != "init" && event.Event != "result" {
		return event.Event
	}
	return ""
}
