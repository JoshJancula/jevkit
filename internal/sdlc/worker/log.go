package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/JoshJancula/jevkit/internal/filelock"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxLogTail is the default number of bytes retained per invocation stream
// (stdout, stderr, and the combined lines.jsonl each independently). It is
// deliberately generous relative to observed invocation output (a single
// CLI agent turn in this codebase's own fixtures runs a few KB to a few
// hundred KB) while still bounding a runaway or looping agent. Request.
// LogTailBytes overrides it per invocation; JEVKIT_SDLC_LOG_TAIL_BYTES is
// the CLI-level override (see cmd/jevkit's sdlcLogTailBytes).
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
	maxTail   int
	omitted   int64 // cumulative bytes dropped from the front of data by tail rollover
	file      *os.File
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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	linesPath := filepath.Join(req.LogDir, id+".lines.jsonl")
	if f, err := os.OpenFile(linesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
		_ = file.Close()
		return nil, err
	} else {
		_ = f.Close()
	}
	maxTail := req.LogTailBytes
	if maxTail <= 0 {
		maxTail = MaxLogTail
	}
	return &invocationLog{path: path, linesPath: linesPath, stream: stream, runtime: req.Agent.Runtime, live: req.LiveOutput, maxTail: maxTail, file: file}, nil
}

// close releases the held file handle for the raw stdout/stderr tail file.
// It does not remove or truncate the file; the last-written tail stays on
// disk exactly as callers (e.g. `sdlc logs`) already expect.
func (l *invocationLog) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
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

// truncationMarker is the unmistakable, byte-accounted line prepended to a
// retained tail once it has dropped older output. It appears identically in
// the raw saved file and in normal (redacted) display, since both read the
// same on-disk bytes; it is never omitted or replaced by "no output".
func truncationMarker(omitted int64) string {
	return fmt.Sprintf("%s %d bytes omitted]\n", TruncationMarkerPrefix, omitted)
}

// TruncationMarkerPrefix identifies the truncation marker line prepended to
// a rolled-over stdout/stderr tail, so callers (e.g. cmd/jevkit's `sdlc
// logs`) can detect and skip past it without depending on the exact
// omitted-byte count it's followed by.
const TruncationMarkerPrefix = "[earlier output truncated:"

func (l *invocationLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data = append(l.data, p...)
	rolledOver := len(l.data) > l.maxTail
	if rolledOver {
		dropped := len(l.data) - l.maxTail
		l.omitted += int64(dropped)
		l.data = append([]byte(nil), l.data[dropped:]...)
		// A rollover changes the truncation marker and the front of the
		// retained window, so the on-disk file must be rewritten. Writes that
		// stay under maxTail (the common case: total output never reaches the
		// 1 MiB default) take the append path below instead, turning what used
		// to be O(total bytes²) full-tail rewrites into O(total bytes) written.
		if err := l.rewriteFile(); err != nil {
			return 0, err
		}
	} else if _, err := l.file.Write(p); err != nil {
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

// rewriteFile replaces the on-disk tail file with the current in-memory
// window (plus the truncation marker, once anything has been dropped). It is
// only reached when a Write rolls the window forward.
func (l *invocationLog) rewriteFile() error {
	data := l.data
	if l.omitted > 0 {
		data = append([]byte(truncationMarker(l.omitted)), data...)
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return err
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	_, err := l.file.Write(data)
	return err
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
	maxCombined := l.maxTail
	if info, err := os.Stat(l.linesPath); os.IsNotExist(err) || err == nil && info.Size()+int64(len(entry)) <= int64(maxCombined) {
		f, err := os.OpenFile(l.linesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = f.Write(entry)
		return err
	}
	data, _ := os.ReadFile(l.linesPath)
	data = append(data, entry...)
	if len(data) > maxCombined {
		before := len(data)
		data = data[len(data)-maxCombined:]
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			data = data[end+1:]
		} else {
			data = nil
		}
		dropped := int64(before - len(data))
		var prevOmitted int64
		if raw, err := os.ReadFile(l.linesPath + ".truncated"); err == nil {
			prevOmitted, _ = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		}
		total := prevOmitted + dropped
		if writeErr := os.WriteFile(l.linesPath+".truncated", []byte(strconv.FormatInt(total, 10)+"\n"), 0o600); writeErr != nil {
			return writeErr
		}
	}
	return os.WriteFile(l.linesPath, data, 0o600)
}
