package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/OWNER/jevkit/internal/filelock"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const MaxLogTail = 1 << 20
const maxReplyCapture = 16 << 20

type captureBuffer struct{ data []byte }

func (b *captureBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > maxReplyCapture {
		b.data = append([]byte(nil), b.data[len(b.data)-maxReplyCapture:]...)
	}
	return len(p), nil
}
func (b *captureBuffer) Bytes() []byte  { return b.data }
func (b *captureBuffer) String() string { return string(b.data) }

// LogMeta identifies the invocation behind a saved stream.
type LogMeta struct {
	Invocation string `json:"invocation"`
	Agent      string `json:"agent"`
	Runtime    string `json:"runtime"`
	StartedAt  string `json:"startedAt"`
}

type invocationLog struct {
	mu        sync.Mutex
	path      string
	linesPath string
	stream    string
	runtime   string
	live      func(stream, line string)
	pending   string
	data      []byte
	truncated bool
}

func newInvocationLog(req Request, stream string) (*invocationLog, error) {
	id := req.Assignment.InvocationID
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return nil, fmt.Errorf("worker: invalid invocation ID")
	}
	if err := os.MkdirAll(req.LogDir, 0o700); err != nil {
		return nil, err
	}
	meta := LogMeta{Invocation: id, Agent: req.Agent.ID, Runtime: req.Agent.Runtime, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	info, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(req.LogDir, id+".json"), info, 0o600); err != nil {
		return nil, err
	}
	path := filepath.Join(req.LogDir, id+"."+stream)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return nil, err
	}
	linesPath := filepath.Join(req.LogDir, id+".lines.jsonl")
	if f, err := os.OpenFile(linesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
		return nil, err
	} else {
		_ = f.Close()
	}
	return &invocationLog{path: path, linesPath: linesPath, stream: stream, runtime: req.Agent.Runtime, live: req.LiveOutput}, nil
}

type LogLine struct {
	At       int64     `json:"at"`
	Stream   string    `json:"stream"`
	Text     string    `json:"text"`
	Activity *Activity `json:"activity,omitempty"`
}

type Activity struct {
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
}

func parseActivity(runtime, line string) *Activity {
	var event struct {
		Type     string                     `json:"type"`
		Subtype  string                     `json:"subtype"`
		Event    string                     `json:"event"`
		ToolCall map[string]json.RawMessage `json:"tool_call"`
		Item     struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Command string `json:"command"`
		} `json:"item"`
		Part struct {
			Type string `json:"type"`
			Tool string `json:"tool"`
		} `json:"part"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return nil
	}
	if event.Type == "" {
		event.Type = event.Event
	}
	if event.Type == "" {
		return nil
	}
	label := event.Item.Type
	if label == "" {
		label = event.Part.Type
	}
	if event.Item.Name != "" {
		label += " " + event.Item.Name
	}
	if event.Part.Tool != "" {
		label += " " + event.Part.Tool
	}
	if event.Type == "tool_call" {
		for name := range event.ToolCall {
			label = strings.TrimSuffix(name, "ToolCall")
			break
		}
		if event.Subtype != "" {
			label += " " + event.Subtype
		}
	}
	if len(label) > 160 {
		label = label[:160]
	}
	return &Activity{Kind: runtime + "/" + event.Type, Label: strings.TrimSpace(label)}
}

func (l *invocationLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data = append(l.data, p...)
	if len(l.data) > MaxLogTail {
		l.data = append([]byte(nil), l.data[len(l.data)-MaxLogTail:]...)
		l.truncated = true
	}
	data := l.data
	if l.truncated {
		data = append([]byte("[earlier output truncated]\n"), data...)
	}
	if err := os.WriteFile(l.path, data, 0o600); err != nil {
		return 0, err
	}
	l.pending += string(p)
	for {
		end := strings.IndexByte(l.pending, '\n')
		if end < 0 {
			break
		}
		line := l.pending[:end]
		l.pending = l.pending[end+1:]
		if err := l.appendLine(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (l *invocationLog) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending == "" {
		return nil
	}
	line := l.pending
	l.pending = ""
	return l.appendLine(line)
}

func (l *invocationLog) appendLine(line string) error {
	if l.live != nil {
		l.live(l.stream, line)
	}
	lock, err := filelock.Acquire(l.linesPath + ".lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	entry, err := json.Marshal(LogLine{At: time.Now().UnixNano(), Stream: l.stream, Text: line, Activity: parseActivity(l.runtime, line)})
	if err != nil {
		return err
	}
	entry = append(entry, '\n')
	const maxCombined = MaxLogTail
	if info, err := os.Stat(l.linesPath); os.IsNotExist(err) || err == nil && info.Size()+int64(len(entry)) <= maxCombined {
		f, err := os.OpenFile(l.linesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(entry)
		return err
	}
	data, _ := os.ReadFile(l.linesPath)
	data = append(data, entry...)
	if len(data) > maxCombined {
		_ = os.WriteFile(l.linesPath+".truncated", []byte("true\n"), 0o600)
		data = data[len(data)-maxCombined:]
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			data = data[end+1:]
		} else {
			data = nil
		}
	}
	return os.WriteFile(l.linesPath, data, 0o600)
}
