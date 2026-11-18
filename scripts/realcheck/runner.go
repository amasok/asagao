package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Runner runs the test asagao (CLI and MCP) and paces the calls for the free plan's
// 150 requests a minute.
type Runner struct {
	Env   Env
	Pause time.Duration // after every command that may reach Asana
	Log   io.Writer

	// RateLimited counts the commands whose output mentions Asana's rate limit (429).
	RateLimited int

	mcp *mcpClient
}

func (r *Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	// --no-context: never read a .asana.json (the working directory is the run's own).
	cmd := exec.CommandContext(ctx, r.Env.Bin, append([]string{"--no-context"}, args...)...)
	cmd.Env = r.Env.Vars
	cmd.Dir = r.Env.Work
	detach(cmd)
	return cmd
}

func rateLimited(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "429") || strings.Contains(l, "rate limit") || strings.Contains(l, "too many requests")
}

func (r *Runner) pause() { time.Sleep(r.Pause) }

// CLI runs `asagao args...` and returns stdout. A non-zero exit is an error carrying the
// output (Asana's error body for `asagao api`).
func (r *Runner) CLI(ctx context.Context, args ...string) ([]byte, error) {
	defer r.pause()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := r.command(ctx, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	fmt.Fprintf(r.Log, "  $ asagao %s\n", strings.Join(args, " "))
	if err != nil && rateLimited(out.String()+errb.String()) {
		r.RateLimited++
	}
	if err != nil {
		return out.Bytes(), fmt.Errorf("asagao %s: %w: %s%s", args[0], err, strings.TrimSpace(out.String()), strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// APIError is Asana's error body as printed by `asagao api`.
type APIError struct {
	Status   string // from asagao's error line "asana: 403 …"
	Messages []string
	Raw      string
}

func (e *APIError) Error() string {
	msg := e.Raw
	if len(e.Messages) > 0 {
		msg = strings.Join(e.Messages, "; ")
	}
	if e.Status != "" {
		return e.Status + " " + msg
	}
	return msg
}

var statusRE = regexp.MustCompile(`asana: ([1-5][0-9][0-9])\b`)

// API calls `asagao api`. Fields are key=value strings (-f). Writes go through guardWrite.
func (r *Runner) API(ctx context.Context, method, path string, fields ...string) (json.RawMessage, error) {
	args := []string{"api", "-X", method, path}
	for _, f := range fields {
		args = append(args, "-f", f)
	}
	out, err := r.CLI(ctx, args...)
	var body struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		} `json:"errors"`
	}
	jerr := json.Unmarshal(out, &body)
	if err != nil || len(body.Errors) > 0 {
		ae := &APIError{Raw: strings.TrimSpace(string(out))}
		if err != nil {
			if m := statusRE.FindStringSubmatch(err.Error()); m != nil {
				ae.Status = m[1]
			}
		}
		for _, e := range body.Errors {
			ae.Messages = append(ae.Messages, e.Message)
		}
		if ae.Raw == "" {
			ae.Raw = err.Error()
		}
		return nil, ae
	}
	if jerr != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, jerr)
	}
	return body.Data, nil
}

// JSON runs a CLI command with --json and decodes stdout into v.
func (r *Runner) JSON(ctx context.Context, v any, args ...string) error {
	out, err := r.CLI(ctx, append([]string{"--json"}, args...)...)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("asagao %s: decode: %w: %.200s", strings.Join(args, " "), err, out)
	}
	return nil
}

// Tool calls an MCP tool on a long-running `asagao mcp` and decodes its structured result.
func (r *Runner) Tool(ctx context.Context, name string, args map[string]any, v any) error {
	defer r.pause()
	if r.mcp == nil {
		c, err := startMCP(ctx, r)
		if err != nil {
			return err
		}
		r.mcp = c
	}
	fmt.Fprintf(r.Log, "  mcp %s %s\n", name, compact(args))
	res, err := r.mcp.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return fmt.Errorf("mcp %s: %w", name, err)
	}
	var tr struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(res, &tr); err != nil {
		return fmt.Errorf("mcp %s: %w", name, err)
	}
	var text []string
	for _, c := range tr.Content {
		text = append(text, c.Text)
	}
	if tr.IsError {
		if rateLimited(strings.Join(text, " ")) {
			r.RateLimited++
		}
		return fmt.Errorf("mcp %s: %s", name, strings.Join(text, " "))
	}
	if v == nil {
		return nil
	}
	src := tr.Structured
	if len(src) == 0 || string(src) == "null" {
		src = json.RawMessage(strings.Join(text, ""))
	}
	if err := json.Unmarshal(src, v); err != nil {
		return fmt.Errorf("mcp %s: decode: %w: %.200s", name, err, src)
	}
	return nil
}

func (r *Runner) Close() {
	if r.mcp != nil {
		r.mcp.close()
		r.mcp = nil
	}
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// mcpClient speaks newline-delimited JSON-RPC to `asagao mcp` over stdio.
type mcpClient struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	mu    sync.Mutex
	next  int
	stder bytes.Buffer
}

func startMCP(ctx context.Context, r *Runner) (*mcpClient, error) {
	cmd := r.command(context.Background(), "mcp") // not ctx: cleanup closes it itself
	c := &mcpClient{cmd: cmd}
	cmd.Stderr = &c.stder
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.in, c.out = in, bufio.NewReaderSize(out, 1<<20)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "asagao-realcheck", "version": "0"},
	}); err != nil {
		c.close()
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	if err := c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *mcpClient) send(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

func (c *mcpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := c.next
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	type reply struct {
		ID     *int            `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	done := make(chan struct{})
	var rep reply
	var rerr error
	go func() {
		defer close(done)
		for {
			line, err := c.out.ReadBytes('\n')
			if err != nil {
				rerr = fmt.Errorf("mcp server: %w (%s)", err, strings.TrimSpace(c.stder.String()))
				return
			}
			var m reply
			if json.Unmarshal(line, &m) != nil || m.ID == nil || *m.ID != id {
				continue // notifications and logs
			}
			rep = m
			return
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		return nil, errors.New("mcp: no reply in 3 minutes")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if rerr != nil {
		return nil, rerr
	}
	if rep.Error != nil {
		return nil, errors.New(rep.Error.Message)
	}
	return rep.Result, nil
}

func (c *mcpClient) close() {
	_ = c.in.Close()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}
