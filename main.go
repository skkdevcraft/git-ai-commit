// git-ai-commit: Prefill Git commit messages using an LLM (OpenAI-compatible API)
// Usage (hook):
//
//	git-ai-commit hook prepare-commit-msg <commit-msg-file> [<source> [<sha>]]
//
// Usage (show):
//
//	git-ai-commit show [--stdin]
//
// Usage (config):
//
//	git-ai-commit config [--global] [--preset openai|anthropic|ollama|lmstudio]
//
// Usage (install):
//
//	git-ai-commit install
//
// Git config keys (suggested):
//
//	ai-commit.endpoint        (required; base URL up to /v1, e.g. https://api.openai.com/v1)
//	ai-commit.model           (e.g. gpt-4o-mini)
//	ai-commit.apiKey          (your API key, or $ENV_VAR, or "git-credentials")
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
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// These variables are set at build time via -ldflags.
// Defaults are used when building locally without GoReleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type config struct {
	Endpoint       string
	Model          string
	APIKey         string
	MaxDiffBytes   int
	TimeoutSeconds int
}

// preset describes a well-known LLM provider configuration.
type preset struct {
	Name        string
	Endpoint    string
	Model       string
	APIKeyHint  string // shown as placeholder if the user hasn't set a key
	Description string
}

var presets = []preset{
	{
		Name:        "openai",
		Endpoint:    "https://api.openai.com/v1",
		Model:       "gpt-4o-mini",
		APIKeyHint:  "sk-...",
		Description: "OpenAI (default)",
	},
	{
		Name:        "anthropic",
		Endpoint:    "https://api.anthropic.com/v1",
		Model:       "claude-sonnet-4-5",
		APIKeyHint:  "sk-ant-...",
		Description: "Anthropic Claude",
	},
	{
		Name:        "ollama",
		Endpoint:    "http://localhost:11434/v1",
		Model:       "llama3",
		APIKeyHint:  "ollama", // Ollama accepts any non-empty string
		Description: "Ollama (local)",
	},
	{
		Name:        "lmstudio",
		Endpoint:    "http://localhost:1234/v1",
		Model:       "local-model",
		APIKeyHint:  "lm-studio", // LM Studio accepts any non-empty string
		Description: "LM Studio (local)",
	},
	{
		Name:        "docker",
		Endpoint:    "http://host.docker.internal:1234/v1",
		Model:       "local-model",
		APIKeyHint:  "lm-studio", // LM Studio accepts any non-empty string
		Description: "LM Studio (local from container)",
	},
}

func findPreset(name string) (preset, bool) {
	for _, p := range presets {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return preset{}, false
}

func main() {
	if len(os.Args) < 2 {
		printUsageAndExit(2)
	}

	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("git-ai-commit %s\n", version)
		fmt.Printf("  commit : %s\n", commit)
		fmt.Printf("  built  : %s\n", date)
		os.Exit(0)

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
		if err := runShow(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "git-ai-commit: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)

	case "config":
		if err := runConfig(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "git-ai-commit: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)

	case "install":
		if err := runInstall(); err != nil {
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
  git-ai-commit config [--global] [--preset openai|anthropic|ollama|lmstudio]
  git-ai-commit install
  git-ai-commit version

Commands:
  hook     Called from the Git prepare-commit-msg hook to prefill the commit
           message editor with an LLM-generated message based on staged diff.
  show     Query the LLM with the current staged diff and print the proposed
           commit message to stdout, without writing any files.
           Pass --stdin to read the diff from standard input instead, e.g.:
             git diff HEAD~3 | git-ai-commit show --stdin
  config   Print the git config commands needed to configure git-ai-commit.
           Copy and paste the output into your terminal to apply the settings.
  install  Install the prepare-commit-msg hook into the current repository.
           Will not overwrite an existing hook. Must be run from inside a
           Git repository.
  version  Print the version of the tool.

Config flags (for config command):
  --global           Add --global to the generated git config commands
                     (writes to ~/.gitconfig instead of the repo's .git/config).
  --preset <name>    Use a preset endpoint/model for a known provider.
                     Available presets: openai, anthropic, ollama, lmstudio

API key (ai-commit.apiKey) — three forms accepted:
  sk-...             A literal key value stored in git config.
  $ENV_VAR           Reads the key from the named environment variable at
                     runtime (e.g. $OPENAI_API_KEY). The dollar sign must be
                     the first character; the variable name follows immediately.
  git-credentials    Delegates to the git credential helper configured for
                     your system. The helper is queried with the protocol and
                     host of ai-commit.endpoint; the password field is used as
                     the API key.`)
	os.Exit(code)
}

// runInstall installs the prepare-commit-msg hook into the current repo's
// .git/hooks directory. It will not overwrite an existing hook file.
func runInstall() error {
	// Find the root of the current git repository.
	gitDir, err := getGitDir()
	if err != nil {
		return fmt.Errorf("not inside a Git repository (or Git is not installed): %w", err)
	}

	hooksDir := filepath.Join(gitDir, "hooks")
	hookFile := filepath.Join(hooksDir, "prepare-commit-msg")

	fmt.Printf("Git directory : %s\n", gitDir)
	fmt.Printf("Hooks directory: %s\n", hooksDir)
	fmt.Printf("Hook file      : %s\n", hookFile)
	fmt.Println()

	// Create the hooks directory if it somehow doesn't exist yet.
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}

	// Refuse to overwrite an existing hook.
	if _, err := os.Stat(hookFile); err == nil {
		// File exists — check whether it already delegates to git-ai-commit.
		existing, readErr := os.ReadFile(hookFile)
		if readErr == nil && strings.Contains(string(existing), "git-ai-commit") {
			fmt.Println("Hook is already installed and references git-ai-commit. Nothing to do.")
			fmt.Printf("  %s\n", hookFile)
			return nil
		}
		return fmt.Errorf(
			"hook file already exists and was not created by git-ai-commit:\n  %s\n\n"+
				"To install manually, add the following line to that file:\n  %s",
			hookFile, hookLine(),
		)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat hook file: %w", err)
	}

	// Write the hook.
	content := hookContent()
	if err := os.WriteFile(hookFile, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write hook file: %w", err)
	}

	// On Windows the executable bit is meaningless, but we set it anyway for
	// consistency; Git for Windows reads the shebang line regardless.
	// On Unix we need the file to be executable — already set via 0o755 above.

	fmt.Printf("Hook installed successfully on %s.\n", osFriendlyName())
	fmt.Println()
	fmt.Println("File created:")
	fmt.Printf("  %s\n", hookFile)
	fmt.Println()
	fmt.Println("Contents written:")
	fmt.Println("  ---")
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		fmt.Printf("  %s\n", line)
	}
	fmt.Println("  ---")
	fmt.Println()
	fmt.Println("Next step: configure your LLM provider by running:")
	fmt.Println("  git-ai-commit config --preset openai   (or anthropic, ollama, lmstudio)")
	return nil
}

// getGitDir returns the absolute path to the .git directory for the current
// working directory. It uses `git rev-parse --git-dir` so it works in
// worktrees and repos with non-standard GIT_DIR locations.
func getGitDir() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	raw := strings.TrimSpace(out.String())
	// The path may be relative (e.g. ".git"); make it absolute.
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// hookContent returns the full text of the prepare-commit-msg hook script,
// adapted for the current operating system.
func hookContent() string {
	switch runtime.GOOS {
	case "windows":
		// Git for Windows ships with a POSIX sh layer, so a sh shebang works.
		// However some users run Git from plain cmd.exe or PowerShell where
		// hook scripts must be .bat/.cmd files — but Git itself always invokes
		// hooks via sh when using Git Bash / MSYS2 / Cygwin, which covers the
		// vast majority of Windows Git installations. We therefore emit the
		// same sh script and add a comment explaining this.
		return "#!/bin/sh\n" +
			"# git-ai-commit prepare-commit-msg hook (Windows / Git for Windows)\n" +
			"# Requires git-ai-commit.exe to be on your PATH.\n" +
			"exec git-ai-commit hook prepare-commit-msg \"$@\"\n"
	default:
		// Linux and macOS.
		return "#!/bin/sh\n" +
			"# git-ai-commit prepare-commit-msg hook\n" +
			"exec git-ai-commit hook prepare-commit-msg \"$@\"\n"
	}
}

// hookLine returns just the exec line, used in error messages.
func hookLine() string {
	return "exec git-ai-commit hook prepare-commit-msg \"$@\""
}

// osFriendlyName returns a human-readable OS label for display purposes.
func osFriendlyName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	default:
		return "Linux"
	}
}

// runConfig prints ready-to-paste git config commands for the user.
func runConfig(args []string) error {
	global := true   // default to --global
	presetName := "" // default to openai

	// Parse flags manually to keep zero dependencies.
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--global":
			global = true
		case "--local":
			global = false
		case "--preset":
			i++
			if i >= len(args) {
				// use openai as default
			} else {
				presetName = args[i]
			}
		default:
			return fmt.Errorf("unknown flag: %s", args[i])
		}
	}

	// Resolve preset (default: openai).
	if presetName == "" {
		presetName = "openai"
	}
	p, ok := findPreset(presetName)
	if !ok {
		names := make([]string, len(presets))
		for i, pr := range presets {
			names[i] = pr.Name
		}
		return fmt.Errorf("unknown preset %q — available: %s", presetName, strings.Join(names, ", "))
	}

	scopeFlag := ""
	scopeLabel := "repo-local"
	if global {
		scopeFlag = "--global "
		scopeLabel = "global (~/.gitconfig)"
	}

	// Derive a conventional environment-variable name from the preset name,
	// e.g. "openai" → "OPENAI_API_KEY", "anthropic" → "ANTHROPIC_API_KEY".
	envVarName := strings.ToUpper(p.Name) + "_API_KEY"

	// Extract protocol and host from the preset endpoint for the git-credentials example.
	endpointURL, _ := url.Parse(p.Endpoint)
	credProtocol := endpointURL.Scheme
	credHost := endpointURL.Hostname()

	isLocalProvider := p.APIKeyHint != "" && !strings.HasPrefix(p.APIKeyHint, "sk-")

	fmt.Printf("# git-ai-commit configuration — %s (%s)\n", p.Description, scopeLabel)
	fmt.Println("# Copy and paste the commands below into your terminal.")
	fmt.Println()

	// ── Option A: literal key ────────────────────────────────────────────────
	fmt.Println("# ── Option A: store a literal API key in git config (simplest, least secure)")
	if isLocalProvider {
		fmt.Printf("# %s accepts any non-empty string as the API key.\n", p.Description)
	}
	fmt.Printf("git config %sai-commit.endpoint %q\n", scopeFlag, p.Endpoint)
	fmt.Printf("git config %sai-commit.model    %q\n", scopeFlag, p.Model)
	fmt.Printf("git config %sai-commit.apiKey   %q\n", scopeFlag, p.APIKeyHint)
	fmt.Println()

	if !isLocalProvider {
		// ── Option B: environment variable ──────────────────────────────────
		fmt.Printf("# ── Option B: read the key from an environment variable at runtime\n")
		fmt.Printf("#    The key never touches disk. Add the export to your shell profile\n")
		fmt.Printf("#    (e.g. ~/.zshrc, ~/.bashrc, ~/.bash_profile) so it is always set.\n")
		fmt.Printf("export %s=\"your-api-key-here\"\n", envVarName)
		fmt.Printf("git config %sai-commit.endpoint %q\n", scopeFlag, p.Endpoint)
		fmt.Printf("git config %sai-commit.model    %q\n", scopeFlag, p.Model)
		fmt.Printf("git config %sai-commit.apiKey   \"$%s\"\n", scopeFlag, envVarName)
		fmt.Println()

		// ── Option C: git credential helper ─────────────────────────────────
		fmt.Printf("# ── Option C: store the key in your OS keychain via git credential helper\n")
		fmt.Printf("#    Run the two commands below once to store the key; git-ai-commit will\n")
		fmt.Printf("#    retrieve it automatically on every commit.\n")
		fmt.Printf("git config %sai-commit.endpoint %q\n", scopeFlag, p.Endpoint)
		fmt.Printf("git config %sai-commit.model    %q\n", scopeFlag, p.Model)
		fmt.Printf("git config %sai-commit.apiKey   \"git-credentials\"\n", scopeFlag)
		fmt.Printf("# Then store the key once:\n")
		fmt.Printf("printf 'protocol=%s\\nhost=%s\\nusername=api-key\\npassword=your-api-key-here\\n' | git credential approve\n",
			credProtocol, credHost)
		fmt.Println()
	}

	fmt.Println("# Optional tuning:")
	fmt.Printf("# git config %sai-commit.maxDiffBytes    \"200000\"\n", scopeFlag)
	fmt.Printf("# git config %sai-commit.timeoutSeconds  \"30\"\n", scopeFlag)
	fmt.Println()
	fmt.Println("# Verify with:")
	fmt.Println("#   git config --list | grep ai-commit")
	fmt.Println()

	// Print all available presets as a reference.
	fmt.Println("# Other available presets (re-run with --preset <name>):")
	for _, pr := range presets {
		marker := "  "
		if pr.Name == p.Name {
			marker = "* "
		}
		fmt.Printf("#   %s%-10s  %s  (%s)\n", marker, pr.Name, pr.Endpoint, pr.Model)
	}

	return nil
}

// runShow generates a commit message from the staged diff and prints it to stdout.
// Unlike the hook path, errors are fatal — the user is explicitly asking for output.
func runShow(args []string) error {
	useStdin := false
	for _, a := range args {
		switch a {
		case "--stdin":
			useStdin = true
		default:
			return fmt.Errorf("unknown flag: %s", a)
		}
	}

	cfg, err := readConfig()
	if err != nil {
		return err
	}

	var diff string
	if useStdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		diff = string(b)
	} else {
		diff, err = getStagedDiff(cfg.MaxDiffBytes)
		if err != nil {
			return err
		}
	}

	if strings.TrimSpace(diff) == "" {
		return errors.New("no diff content — either stage some changes or pipe a diff via --stdin")
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

	if cfg.Endpoint == "" {
		return cfg, errors.New("missing git config: ai-commit.endpoint (set to base URL, e.g. https://api.openai.com/v1)")
	}
	if cfg.Model == "" {
		// local endpoints may be ok with no model provided...
	}

	// Normalise: resolve to the canonical /chat/completions URL,
	// handling any combination of trailing slashes, existing /v1, etc.
	// We do this before resolving the API key so that git-credentials can use
	// the normalised endpoint URL.
	resolved, err := ResolveChatCompletionsEndpoint(cfg.Endpoint)
	if err != nil {
		return cfg, fmt.Errorf("invalid ai-commit.endpoint %q: %w", cfg.Endpoint, err)
	}
	cfg.Endpoint = resolved

	// Resolve the API key — may be a literal value, an env-var reference, or
	// the special token "git-credentials".
	if rawKey, ok := gitConfigGet("ai-commit.apiKey"); ok {
		key, err := resolveAPIKey(strings.TrimSpace(rawKey), cfg.Endpoint)
		if err != nil {
			return cfg, fmt.Errorf("ai-commit.apiKey: %w", err)
		}
		cfg.APIKey = key
	}
	// If ai-commit.apiKey is not set at all we leave cfg.APIKey empty;
	// local endpoints (Ollama, LM Studio) work fine without one.

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

	return cfg, nil
}

// resolveAPIKey resolves the raw value of ai-commit.apiKey into an actual key
// string. Three forms are supported:
//
//  1. Literal — any value that does not match the forms below is returned as-is.
//  2. Env-var — a value starting with "$" is treated as an environment-variable
//     name; the variable is read at runtime.
//     Example config value: $OPENAI_API_KEY
//  3. git-credentials — the exact string "git-credentials" (case-insensitive)
//     causes the git credential helper to be queried using the protocol and
//     host extracted from endpoint; the returned password is used as the key.
func resolveAPIKey(raw, endpoint string) (string, error) {
	if raw == "" {
		return "", nil
	}

	// Form 2: environment variable reference.
	if strings.HasPrefix(raw, "$") {
		varName := raw[1:]
		if varName == "" {
			return "", errors.New("environment variable name must not be empty (got bare \"$\")")
		}
		val := os.Getenv(varName)
		if val == "" {
			return "", fmt.Errorf("environment variable %q is not set or is empty", varName)
		}
		return val, nil
	}

	// Form 3: git credential helper.
	if strings.EqualFold(raw, "git-credentials") {
		return resolveAPIKeyFromGitCredentials(endpoint)
	}

	// Form 1: literal value.
	return raw, nil
}

// resolveAPIKeyFromGitCredentials asks the configured git credential helper for
// the password associated with the host of endpoint, then returns it as the API
// key. It shells out to `git credential fill`, which consults the same helpers
// that Git itself uses (macOS Keychain, Windows Credential Manager, libsecret,
// pass, etc.).
//
// The "username" field in the credential request is set to "api-key" as a
// conventional label; most helpers store credentials by (protocol, host,
// username) so this keeps LLM keys separate from any Git hosting credentials
// that may share the same hostname.
func resolveAPIKeyFromGitCredentials(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("cannot parse endpoint URL for git-credentials lookup: %w", err)
	}

	protocol := u.Scheme
	host := u.Hostname()
	port := u.Port()

	if protocol == "" || host == "" {
		return "", fmt.Errorf("endpoint %q has no scheme or host; cannot query git credential helper", endpoint)
	}

	// Build the input for `git credential fill`.
	// Format: key=value pairs, one per line, terminated by a blank line.
	var input strings.Builder
	fmt.Fprintf(&input, "protocol=%s\n", protocol)
	fmt.Fprintf(&input, "host=%s\n", host)
	if port != "" {
		fmt.Fprintf(&input, "host=%s:%s\n", host, port) // some helpers want host:port
	}
	fmt.Fprintf(&input, "username=api-key\n")
	fmt.Fprintf(&input, "\n")

	cmd := exec.Command("git", "credential", "fill")
	cmd.Stdin = strings.NewReader(input.String())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		stderr := strings.TrimSpace(errBuf.String())
		if stderr != "" {
			return "", fmt.Errorf("git credential fill failed: %w: %s", err, stderr)
		}
		return "", fmt.Errorf("git credential fill failed: %w", err)
	}

	// Parse the output: lines of "key=value".
	password := ""
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "password=") {
			password = strings.TrimPrefix(line, "password=")
			break
		}
	}

	if password == "" {
		return "", fmt.Errorf(
			"git credential fill returned no password for protocol=%s host=%s username=api-key\n"+
				"Store the key with: git credential approve  (or use your system keychain tool)",
			protocol, host,
		)
	}

	return password, nil
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
- Do not use emoji anywhere in the output.
- Do not use any quotation marks (single, double, or backticks) in the output.
- Do not use backslashes or any other escape characters in the output.
- The output must be safe to copy and paste directly into a terminal without any shell interpretation issues.

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

func ResolveChatCompletionsEndpoint(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}

	// Normalize path
	cleanPath := path.Clean("/" + strings.TrimPrefix(u.Path, "/"))

	// Remove existing /chat/completions if already present
	cleanPath = strings.TrimSuffix(cleanPath, "/chat/completions")

	// Ensure we have /v1
	if !strings.HasSuffix(cleanPath, "/v1") {
		cleanPath = path.Join(cleanPath, "v1")
	}

	// Append final path
	cleanPath = path.Join(cleanPath, "chat", "completions")

	u.Path = cleanPath
	u.RawQuery = "" // Defensive: remove accidental query params

	return u.String(), nil
}
