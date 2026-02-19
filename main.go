// git-ai-commit: Prefill Git commit messages using an LLM (OpenAI-compatible API)
// Usage (hook):
//
//	git-ai-commit hook prepare-commit-msg <commit-msg-file> [<source> [<sha>]]
//
// Usage (show):
//
//	git-ai-commit show
//
// Git config keys (suggested):
//
//	ai-commit.endpoint        (required; base URL up to /v1, e.g. https://api.openai.com/v1)
//	ai-commit.model           (e.g. gpt-4o-mini)
//	ai-commit.apiKey          (your API key)
//	ai-commit.maxDiffBytes    (optional, int; default 200000)
//	ai-commit.timeoutSeconds  (optional, int; default 30)
//
// Hook example (.git/hooks/prepare-commit-msg):
//
//	#!/bin/sh
//	exec git-ai-commit hook prepare-commit-msg "$@"
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Endpoint       string
	Model          string
	APIKey         string
	MaxDiffBytes   int
	TimeoutSeconds int
}

func main() {
	if len(os.Args) < 2 {
		printUsageAndExit(2)
	}

	switch os.Args[1] {
	case "hook":
		if len(os.Args) < 3 {
			printUsageAndExit(2)
		}
		if os.Args[2] != "prepare-commit-msg" {
			fatalf(2, "unsupported hook: %s", os.Args[2])
		}
		if err := runPrepareCommitMsg(os.Args[3:]); err != nil {
			// In hook mode, default to non-blocking behavior:
			// do not prevent commits if LLM/network/config fails.
			// Print to stderr for visibility, then exit 0.
			fmt.Fprintf(os.Stderr, "git-ai-commit: %v\n", err)
			os.Exit(0)
		}
		os.Exit(0)

	case "show":
		if err := runShow(); err != nil {
			fmt.Fprintf(os.Stderr, "git-ai-commit: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)

	case "--help", "-h", "help":
		printUsageAndExit(0)

	default:
		fatalf(2, "unknown command: %s\n(try: git-ai-commit hook prepare-commit-msg ... or git-ai-commit show)", os.Args[1])
	}
}

func printUsageAndExit(code int) {
	out := os.Stdout
	if code != 0 {
		out = os.Stderr
	}
	fmt.Fprintln(out, `git-ai-commit

Usage:
  git-ai-commit hook prepare-commit-msg <commit-msg-file> [<source> [<sha>]]
  git-ai-commit show

Commands:
  hook    Called from the Git prepare-commit-msg hook to prefill the commit
          message editor with an LLM-generated message based on staged diff.
  show    Query the LLM with the current staged diff and print the proposed
          commit message to stdout, without writing any files.`)
	os.Exit(code)
}

// runShow generates a commit message from the staged diff and prints it to stdout.
// Unlike the hook path, errors are fatal — the user is explicitly asking for output.
func runShow() error {
	cfg, err := readConfig()
	if err != nil {
		return err
	}

	diff, err := getStagedDiff(cfg.MaxDiffBytes)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		return errors.New("no staged changes found — did you forget to git add?")
	}

	prompt := buildPrompt(diff)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	fmt.Fprintf(os.Stderr, "Querying %s (%s)...\n", cfg.Endpoint, cfg.Model)

	msg, err := callChatCompletions(ctx, cfg, prompt)
	if err != nil {
		return err
	}
	msg = sanitizeCommitMessage(msg)
	if msg == "" {
		return errors.New("LLM returned empty commit message")
	}

	fmt.Print(msg)
	return nil
}

func runPrepareCommitMsg(args []string) error {
	if len(args) < 1 {
		return errors.New("prepare-commit-msg requires <commit-msg-file>")
	}
	msgFile := args[0]
	source := ""
	if len(args) >= 2 {
		source = args[1]
	}

	// Common skip cases:
	// - merge/squash: Git is constructing special commit messages.
	if source == "merge" || source == "squash" {
		return nil
	}

	// If the message file already has meaningful content (e.g. -m, template already filled),
	// do nothing.
	existing, err := os.ReadFile(msgFile)
	if err != nil {
		return fmt.Errorf("read commit message file: %w", err)
	}
	if hasNonCommentContent(string(existing)) {
		return nil
	}

	cfg, err := readConfig()
	if err != nil {
		return err
	}

	diff, err := getStagedDiff(cfg.MaxDiffBytes)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		return nil
	}

	prompt := buildPrompt(diff)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	msg, err := callChatCompletions(ctx, cfg, prompt)
	if err != nil {
		return err
	}
	msg = sanitizeCommitMessage(msg)
	if msg == "" {
		return errors.New("LLM returned empty commit message")
	}

	// Preserve any existing content (likely Git comments/instructions).
	// Since we've verified there's no meaningful content, we can safely place our message on top.
	newBody := msg
	if !strings.HasSuffix(newBody, "\n") {
		newBody += "\n"
	}
	// Ensure one blank line before any existing comment block (if present).
	if strings.TrimSpace(string(existing)) != "" {
		if !strings.HasSuffix(newBody, "\n\n") {
			newBody += "\n"
		}
		newBody += string(existing)
	}

	if err := os.WriteFile(msgFile, []byte(newBody), 0o644); err != nil {
		return fmt.Errorf("write commit message file: %w", err)
	}
	return nil
}

func readConfig() (config, error) {
	cfg := config{
		Endpoint:       "https://api.openai.com/v1",
		Model:          "gpt-5-nano",
		MaxDiffBytes:   200_000,
		TimeoutSeconds: 30,
	}

	if v, ok := gitConfigGet("ai-commit.endpoint"); ok && strings.TrimSpace(v) != "" {
		cfg.Endpoint = strings.TrimSpace(v)
	}
	if v, ok := gitConfigGet("ai-commit.model"); ok {
		cfg.Model = strings.TrimSpace(v)
	}
	if v, ok := gitConfigGet("ai-commit.apiKey"); ok {
		cfg.APIKey = strings.TrimSpace(v)
	}

	if v, ok := gitConfigGet("ai-commit.maxDiffBytes"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			cfg.MaxDiffBytes = n
		}
	}
	if v, ok := gitConfigGet("ai-commit.timeoutSeconds"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			cfg.TimeoutSeconds = n
		}
	}

	if cfg.Endpoint == "" {
		return cfg, errors.New("missing git config: ai-commit.endpoint (set to base URL, e.g. https://api.openai.com/v1)")
	}
	if cfg.Model == "" {
		return cfg, errors.New("missing git config: ai-commit.model")
	}
	if cfg.APIKey == "" {
		return cfg, errors.New("missing git config: ai-commit.apiKey")
	}

	// Normalise: strip trailing slash, then append the fixed path.
	// User provides the base URL up to /v1, e.g. https://api.openai.com/v1
	base := strings.TrimRight(cfg.Endpoint, "/")
	cfg.Endpoint = base + "/chat/completions"

	return cfg, nil
}

func gitConfigGet(key string) (string, bool) {
	// Uses the effective config (system + global + local), which is usually what you want.
	// If the key is unset, git exits non-zero; we treat that as "not found".
	cmd := exec.Command("git", "config", "--get", key)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return strings.TrimRight(out.String(), "\n"), true
}

func getStagedDiff(maxBytes int) (string, error) {
	// Staged diff only, and disable color/ext diff to keep prompts clean and deterministic.
	cmd := exec.Command("git", "diff", "--cached", "--no-color", "--no-ext-diff")
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff --cached failed: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}

	b := out.Bytes()
	if maxBytes > 0 && len(b) > maxBytes {
		// Truncate safely. Add a marker so the model knows it's incomplete.
		trunc := b[:maxBytes]
		return string(trunc) + "\n\n[diff truncated]\n", nil
	}
	return string(b), nil
}

func buildPrompt(diff string) string {
	// Keep prompt simple and instruction-focused.
	return strings.TrimSpace(fmt.Sprintf(`
You are an expert software engineer. Write a Git commit message for the following staged diff.

Requirements:
- Output plain text only.
- First line: a concise subject following the Conventional Commits format, max 72 characters.
  The subject must start with one of these types followed by a colon and a space:
    feat:     a new feature
    fix:      a bug fix
    docs:     documentation changes only
    style:    formatting, whitespace — no logic change
    refactor: code restructured without adding features or fixing bugs
    perf:     performance improvement
    test:     adding or updating tests
    chore:    build process, tooling, dependency updates, CI config
  Use a scope in parentheses when it helps clarity, e.g. "feat(auth): add OAuth2 login".
  Write the description in imperative mood, e.g. "feat: add retry logic" not "feat: added retry logic".
- Then a blank line.
- Then 3-7 bullet points ("- ") summarizing key changes.
- Mention user-visible behavior changes and important refactors.
- Do not include code fences.

Staged diff:
%s
`, diff))
}

type chatCompletionsRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func callChatCompletions(ctx context.Context, cfg config, prompt string) (string, error) {
	reqBody := chatCompletionsRequest{
		Model: cfg.Model,
		Messages: []message{
			{Role: "system", Content: "You write concise, high-signal Git commit messages."},
			{Role: "user", Content: prompt},
		},
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint, bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // cap 4MB
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Try to parse error shape; fall back to raw body.
		var parsed chatCompletionsResponse
		if json.Unmarshal(body, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
			return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, parsed.Error.Message)
		}
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatCompletionsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse response: %w (body: %s)", err, strings.TrimSpace(string(body)))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("LLM error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("LLM response missing choices")
	}

	return parsed.Choices[0].Message.Content, nil
}

func sanitizeCommitMessage(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)

	// Some models might return surrounding quotes; strip common cases.
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// Ensure it ends with a newline (Git is fine either way, but this is tidy).
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

func hasNonCommentContent(commitMsg string) bool {
	commitMsg = strings.ReplaceAll(commitMsg, "\r\n", "\n")
	for _, line := range strings.Split(commitMsg, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "#") {
			continue
		}
		return true
	}
	return false
}

func fatalf(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "git-ai-commit: "+format+"\n", args...)
	os.Exit(code)
}
