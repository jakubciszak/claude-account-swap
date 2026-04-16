# cswap

Multi-account switcher for Claude Code. Rotate between multiple Claude accounts without logging out.

## Features

- Switch between unlimited Claude Code accounts
- Real-time usage stats (5h / 7d utilization with reset countdowns)
- macOS Keychain integration (file-based on Linux/WSL)
- Organization + personal account support
- Transaction rollback on failed switches
- Zero dependencies — single static binary

## Installation

### Option A: Download binary

Download the binary for your platform from `dist/`:

| Platform | File |
|----------|------|
| macOS Apple Silicon | `cswap-darwin-arm64` |
| macOS Intel | `cswap-darwin-amd64` |
| Linux x86_64 | `cswap-linux-amd64` |
| Linux ARM64 | `cswap-linux-arm64` |

Then:

```bash
# Copy to a directory in your PATH
cp cswap-darwin-arm64 ~/.local/bin/cswap
chmod +x ~/.local/bin/cswap
```

### Option B: Build from source

Requires Go 1.22+.

```bash
git clone <repo-url>
cd claude-swap
go install ./cmd/cswap/

# Make sure ~/go/bin is in your PATH, or symlink:
ln -s ~/go/bin/cswap ~/.local/bin/cswap
```

## Usage

### 1. Add accounts

Log into Claude Code with your first account, then:

```bash
cswap --add-account
```

Log out, log in with the second account, repeat:

```bash
cswap --add-account
```

### 2. Switch accounts

Rotate to next account:

```bash
cswap --switch
```

Switch to a specific account:

```bash
cswap --switch-to 1
cswap --switch-to user@example.com
```

> After switching, restart Claude Code for the new account to take effect.

### 3. Other commands

```bash
cswap --list              # List accounts with live usage stats
cswap --status            # Show current active account
cswap --remove-account 2  # Remove account by number or email
cswap --purge             # Remove all cswap data
cswap --debug --list      # Enable debug logging
```

## How it works

- Account credentials and configs are backed up to `~/.claude-swap-backup/`
- On macOS, credentials are stored in the system Keychain
- On Linux/WSL, credentials are stored as base64-encoded files with 0600 permissions
- Switching replaces Claude Code's active credentials and `oauthAccount` config
- All switch operations use file locking and transaction rollback for safety

## Refreshing tokens

If a token expires, just log back into Claude Code with that account and run:

```bash
cswap --add-account
```

This updates the stored credentials without creating a duplicate.
