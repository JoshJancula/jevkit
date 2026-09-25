package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// CompactCodex runs Codex's native app-server compaction protocol for an
// explicit thread ID. Completion, rather than request acceptance, is required.
func CompactCodex(ctx context.Context, binary, workDir, threadID string) error {
	if binary == "" {
		binary = "codex"
	}
	if threadID == "" {
		return fmt.Errorf("codex compact: missing thread ID")
	}
	cmd := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	cmd.Dir = workDir
	prepareRuntimeCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr captureBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	write := func(id int, method string, params any) error {
		data, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(data, '\n'))
		return err
	}
	if err := write(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "jevkit", "title": "Jevkit SDLC", "version": "1.0"}, "capabilities": map[string]any{}}); err != nil {
		return err
	}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 4096), 1<<20)
	stage := 1
	for scan.Scan() {
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
			Params struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					Status string `json:"status"`
				} `json:"turn"`
			} `json:"params"`
		}
		if json.Unmarshal(bytes.TrimSpace(scan.Bytes()), &msg) != nil {
			continue
		}
		if msg.ID != nil && *msg.ID == stage {
			if msg.Error != nil {
				return fmt.Errorf("codex compact: %s", msg.Error.Message)
			}
			switch stage {
			case 1:
				data, _ := json.Marshal(map[string]any{"method": "initialized"})
				if _, err := stdin.Write(append(data, '\n')); err != nil {
					return err
				}
				if err := write(2, "thread/resume", map[string]string{"threadId": threadID}); err != nil {
					return err
				}
				stage = 2
			case 2:
				if err := write(3, "thread/compact/start", map[string]string{"threadId": threadID}); err != nil {
					return err
				}
				stage = 3
			case 3:
				stage = 4
			}
			continue
		}
		if stage == 4 && msg.Method == "turn/completed" && msg.Params.ThreadID == threadID {
			if msg.Params.Turn.Status != "completed" {
				return fmt.Errorf("codex compact: turn ended with %s", msg.Params.Turn.Status)
			}
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scan.Err(); err != nil {
		return err
	}
	return fmt.Errorf("codex compact: app server closed before completion: %s", strings.TrimSpace(truncate(stderr.String(), 300)))
}
