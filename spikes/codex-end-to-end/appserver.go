// This driver is a copy of the one in spikes/codex-compatibility. That harness
// is the frozen artifact behind ADR-0012 and is named in the Slice 0.4 results,
// so it is left byte-stable rather than refactored into a shared package. The
// duplication is deliberate and confined to spike code.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// rpcMessage is one newline-delimited JSON-RPC message from the Codex app
// server. Responses carry an id; notifications carry only a method.
type rpcMessage struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  json.RawMessage  `json:"error,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
}

// appServerClient drives `codex app-server` over stdio. It is the only way found
// to exercise the real Codex MCP client without starting a model turn.
type appServerClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *strings.Builder

	mu       sync.Mutex
	nextID   int
	pending  map[int]chan rpcMessage
	notifies []string
	closed   bool
}

func startAppServer(ctx context.Context, codexPath, workDir string, env []string) (*appServerClient, error) {
	cmd := exec.CommandContext(ctx, codexPath, "app-server", "--listen", "stdio://")
	cmd.Dir = workDir
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	c := &appServerClient{
		cmd:     cmd,
		stdin:   stdin,
		stderr:  &strings.Builder{},
		nextID:  0,
		pending: make(map[int]chan rpcMessage),
	}

	go c.readLoop(stdout)
	go func() {
		// Bounded: the harness only needs the tail for diagnostics.
		buf := make([]byte, 64*1024)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				c.mu.Lock()
				if c.stderr.Len() < 32*1024 {
					c.stderr.Write(buf[:n])
				}
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	return c, nil
}

func (c *appServerClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.ID == nil {
			if msg.Method != "" {
				c.mu.Lock()
				c.notifies = append(c.notifies, msg.Method)
				c.mu.Unlock()
			}
			continue
		}
		var id int
		if err := json.Unmarshal(*msg.ID, &id); err != nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ok {
			ch <- msg
			close(ch)
		}
	}
	// Fail any still-pending calls once stdout ends.
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	c.mu.Unlock()
}

// call sends a request and waits for the matching response.
func (c *appServerClient) call(ctx context.Context, method string, params any) (rpcMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return rpcMessage{}, errors.New("app server stdout already closed")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload := map[string]any{"id": id, "method": method, "params": params}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return rpcMessage{}, err
	}
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		return rpcMessage{}, fmt.Errorf("write %s: %w", method, err)
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return rpcMessage{}, fmt.Errorf("%s: app server closed before responding", method)
		}
		return msg, nil
	case <-ctx.Done():
		return rpcMessage{}, fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

func (c *appServerClient) notifications() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.notifies))
	copy(out, c.notifies)
	return out
}

func (c *appServerClient) stderrTail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

func (c *appServerClient) close() {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}
