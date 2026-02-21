# git-ai-commit

Automatically prefill your Git commit messages using an LLM. When you run `git commit`, the editor opens with a generated message based on your staged diff — following the [Conventional Commits](https://www.conventionalcommits.org) format, with a concise subject line and a short bullet-point body.

Works with any OpenAI-compatible API: OpenAI, Anthropic Claude, Ollama, LM Studio, and others.

---

## How it works

1. You stage your changes with `git add`.
2. Run `git-ai-commit show`, which show the commit message as an output.
3. Copy the commit message and use it in the git UI of your choice.

Alternatively, you can use the tool as a `hook` (see `git-ai-commit install`):

1. You stage your changes with `git add`.
2. You run `git commit` as usual.
3. The `prepare-commit-msg` hook calls `git-ai-commit`, which sends your staged
   diff to the configured LLM.
4. The editor opens pre-filled with the generated message.
5. You review, edit if needed, and save — done.

If the LLM or network is unavailable, the hook exits cleanly and Git opens the editor with a blank message as normal. It never blocks a commit.

---

## Installation

### 1. Install the binary

Choose the method that matches your system.

---

### Option A — Homebrew (macOS, recommended)

```sh
brew install skkdevcraft/tap/git-ai-commit
```

Verify:

```sh
git-ai-commit --help
```

---

### Option B — One-line install (Linux / macOS)

#### Linux (x86_64)

```sh
curl -fsSL https://github.com/skkdevcraft/git-ai-commit/releases/latest/download/git-ai-commit_Linux_x86_64.tar.gz | tar -xz && sudo mv git-ai-commit /usr/local/bin/
```

#### Linux (arm64)

```sh
curl -fsSL https://github.com/skkdevcraft/git-ai-commit/releases/latest/download/git-ai-commit_Linux_arm64.tar.gz | tar -xz && sudo mv git-ai-commit /usr/local/bin/
```

#### macOS (Apple Silicon)

```sh
curl -fsSL https://github.com/skkdevcraft/git-ai-commit/releases/latest/download/git-ai-commit_Darwin_arm64.tar.gz | tar -xz && sudo mv git-ai-commit /usr/local/bin/
```

If you prefer installing without `sudo`:

```sh
mkdir -p ~/.local/bin && \
curl -fsSL https://github.com/skkdevcraft/git-ai-commit/releases/latest/download/git-ai-commit_$(uname -s)_$(uname -m | sed 's/x86_64/x86_64/;s/arm64/arm64/').tar.gz \
| tar -xz && mv git-ai-commit ~/.local/bin/
```

Make sure `~/.local/bin` is on your `PATH`.

---

### Windows (PowerShell, one line)

#### Windows (x86_64)

```powershell
iwr https://github.com/skkdevcraft/git-ai-commit/releases/latest/download/git-ai-commit_Windows_x86_64.zip -OutFile git-ai-commit.zip; Expand-Archive git-ai-commit.zip -Force; $extractPath = (Get-Location).Path; $env:Path += ";$extractPath\git-ai-commit"; [Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$extractPath\git-ai-commit", "User")
```

This command downloads the tool, extracts it and places the extracted path in the PATH environment variable.

---

### Option C — Install with `go install` (requires Go 1.21+)

```sh
go install github.com/skkdevcraft/git-ai-commit@latest
```

The binary is placed in:

```
$(go env GOPATH)/bin
```

Make sure it is on your `PATH`:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

### 2. Configure your LLM provider

After the tool is installed, you need to tell it which LLM provider to use:

Run the `config` command to print ready-to-paste `git config` settings for your provider. The generated commands write to your global `~/.gitconfig` by default so every repository on your machine can use them.

```sh
# OpenAI (default)
git-ai-commit config --preset openai

# Anthropic Claude
git-ai-commit config --preset anthropic

# Ollama (local)
git-ai-commit config --preset ollama

# LM Studio (local)
git-ai-commit config --preset lmstudio
```

The `config` command prints three ready-to-paste options for storing your API key, from simplest to most secure. Pick one and run those commands. See [API key configuration](#api-key-configuration) for a full explanation of each option.

Optional tuning (defaults shown):

```sh
git config --global ai-commit.maxDiffBytes   "200000"  # truncate large diffs
git config --global ai-commit.timeoutSeconds "30"      # LLM request timeout
```

Verify your configuration:

```sh
git config --list | grep ai-commit
```

---

### 3. Install the Git hook

Inside any repository where you want AI-generated commit messages, run:

```sh
git-ai-commit install
```

This creates `.git/hooks/prepare-commit-msg` and shows you exactly what was written and where:

```
Git directory  : /your/project/.git
Hooks directory: /your/project/.git/hooks
Hook file      : /your/project/.git/hooks/prepare-commit-msg

Hook installed successfully on macOS.

File created:
  /your/project/.git/hooks/prepare-commit-msg

Contents written:
  ---
  #!/bin/sh
  # git-ai-commit prepare-commit-msg hook
  exec git-ai-commit hook prepare-commit-msg "$@"
  ---

Next step: configure your LLM provider by running:
  git-ai-commit config --preset openai   (or anthropic, ollama, lmstudio)
```

The install command will **not overwrite** an existing hook. If you already have a `prepare-commit-msg` hook, it prints the single line you need to add to it manually.

To apply the hook to all future repositories automatically, configure a global Git hook template directory:

```sh
mkdir -p ~/.git-templates/hooks
git config --global init.templateDir ~/.git-templates
git-ai-commit install   # run once from any repo; then copy the hook:
cp .git/hooks/prepare-commit-msg ~/.git-templates/hooks/
chmod +x ~/.git-templates/hooks/prepare-commit-msg
```

New repositories created with `git init` will inherit the hook automatically.

---

## Usage

### Normal commit flow

```sh
git add .
git commit        # editor opens pre-filled with the generated message
```

Review the message, edit if you like, save and close the editor to complete the commit.

### Preview without committing

Print the generated message to stdout without touching any files:

```sh
git-ai-commit show
```

### Preview from a custom diff

Pass `--stdin` to read the diff from standard input instead of the current staged changes. This lets you generate a commit message for any diff — not just what is currently staged:

```sh
# Generate a message for the last 3 commits
git diff HEAD~3 | git-ai-commit show --stdin

# Generate a message for changes between two branches
git diff main..feature/my-branch | git-ai-commit show --stdin

# Generate a message from a saved diff file
git-ai-commit show --stdin < my-changes.patch
```

When using `--stdin`, `ai-commit.maxDiffBytes` is not applied — you control what you pipe in.

### Skip the generated message for a single commit

Pass `-m` to provide your own message — the hook detects existing content and skips the LLM call:

```sh
git commit -m "chore: manual message, no LLM needed"
```

---

## Commands

| Command | Description |
|---|---|
| `git-ai-commit install` | Install the hook into the current repository |
| `git-ai-commit config [--global] [--preset NAME]` | Print ready-to-paste config commands |
| `git-ai-commit show [--stdin]` | Generate and print a commit message for the current staged diff, or for a diff piped via stdin |
| `git-ai-commit hook prepare-commit-msg FILE [SOURCE [SHA]]` | Called by Git directly; normally not invoked by hand |

---

## Available presets

| Preset | Endpoint | Default model |
|---|---|---|
| `openai` | https://api.openai.com/v1 | gpt-4o-mini |
| `anthropic` | https://api.anthropic.com/v1 | claude-sonnet-4-5 |
| `ollama` | http://localhost:11434/v1 | llama3 |
| `lmstudio` | http://localhost:1234/v1 | local-model |
| `docker` | http://host.docker.internal:1234/v1 | local-model |

You can override the model after applying a preset:

```sh
git config --global ai-commit.model "gpt-4o"
```

---

## API key configuration

The `ai-commit.apiKey` config value accepts three forms. Run `git-ai-commit config --preset <name>` to get copy-pasteable commands for all three options tailored to your chosen provider.

### Option A — Literal key (simplest)

Store the key directly in git config. This is the quickest option but the key is written to `~/.gitconfig` in plain text.

```sh
git config --global ai-commit.endpoint "https://api.openai.com/v1"
git config --global ai-commit.model    "gpt-4o-mini"
git config --global ai-commit.apiKey   "sk-your-real-key-here"
```

### Option B — Environment variable (recommended)

Set the key in your shell profile so it is never written to disk by this tool. The config value must start with `$`; the rest is the variable name.

```sh
# Add to ~/.zshrc or ~/.bashrc:
export OPENAI_API_KEY="sk-your-real-key-here"
```

```sh
git config --global ai-commit.endpoint "https://api.openai.com/v1"
git config --global ai-commit.model    "gpt-4o-mini"
git config --global ai-commit.apiKey   "$OPENAI_API_KEY"
```

At runtime, git-ai-commit reads the variable from the environment. If the variable is unset or empty, the tool reports an error instead of silently failing.

### Option C — Git credential helper (most secure)

Use the exact string `git-credentials` as the value. git-ai-commit will call `git credential fill` at runtime, querying whatever credential helper your system has configured — macOS Keychain, Windows Credential Manager, GNOME libsecret, `pass`, and so on.

Store the key once using `git credential approve`:

```sh
printf 'protocol=https\nhost=api.openai.com\nusername=api-key\npassword=sk-your-real-key-here\n' \
  | git credential approve
```

Then point the config at the helper:

```sh
git config --global ai-commit.endpoint "https://api.openai.com/v1"
git config --global ai-commit.model    "gpt-4o-mini"
git config --global ai-commit.apiKey   "git-credentials"
```

The key is retrieved from your OS keychain on every commit and is never stored in any config file. The `username=api-key` label is a convention used to keep LLM credentials separate from any Git hosting credentials on the same host.

> **Note:** Local providers such as Ollama and LM Studio do not require a real API key. For those presets the `config` command only shows Option A, using a placeholder value that the provider accepts.

---

## Commit message format

Generated messages follow [Conventional Commits](https://www.conventionalcommits.org):

```
feat(auth): add OAuth2 login support

- Add OAuth2 provider configuration to auth package
- Implement token exchange and refresh flow
- Store tokens securely using the system keychain
- Expose new login and logout commands on the CLI
- Update README with OAuth2 setup instructions
```

Supported types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `chore`.

---

## Configuration reference

All keys are read from standard Git config (system, global, or local).

| Key | Required | Default | Description |
|---|---|---|---|
| `ai-commit.endpoint` | yes | `https://api.openai.com/v1` | Base URL of the OpenAI-compatible API |
| `ai-commit.model` | no | `gpt-5-nano` | Model name to use |
| `ai-commit.apiKey` | no | _(empty)_ | API key — literal value, `$ENV_VAR`, or `git-credentials` |
| `ai-commit.maxDiffBytes` | no | `200000` | Truncate diffs larger than this (bytes); not applied when using `--stdin` |
| `ai-commit.timeoutSeconds` | no | `30` | HTTP timeout for the LLM request |

---

## Troubleshooting

**The hook runs but nothing is generated.**
Check that `git-ai-commit` is on your `PATH` by running `git-ai-commit --help` from the same shell you use to commit. Then verify your config with `git config --list | grep ai-commit`.

**LLM HTTP 401 / authentication error.**
Your API key is missing or incorrect. Re-run `git-ai-commit config --preset openai` (or your provider), update the `apiKey` value, and apply the command.

**Environment variable not found.**
If you set `ai-commit.apiKey` to a `$VAR` reference, make sure the variable is exported in the shell that runs the hook (not just in your interactive shell). Add the `export` line to your shell profile (`~/.zshrc`, `~/.bashrc`, etc.) rather than setting it only for the current session.

**git credential fill returns no password.**
Run the `git credential approve` command from Option C above, substituting your actual key for the `password` field. You can verify the credential was stored with:
```sh
printf 'protocol=https\nhost=api.openai.com\nusername=api-key\n\n' | git credential fill
```

**LLM request timed out.**
Increase the timeout: `git config --global ai-commit.timeoutSeconds "60"`. For local models (Ollama, LM Studio) make sure the server is running before committing.

**Hook already exists error.**
You already have a `prepare-commit-msg` hook. Open the file and add this line (after any existing logic):

```sh
exec git-ai-commit hook prepare-commit-msg "$@"
```

**Windows: hook does not run.**
Ensure you are using Git for Windows (Git Bash / MSYS2). The hook script uses a `#!/bin/sh` shebang which requires the POSIX shell layer bundled with Git for Windows. Plain `cmd.exe` without Git Bash will not invoke the hook correctly.

---

## License

MIT