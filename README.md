> **Fork notice:** This is a fork of [boldsoftware/shelley](https://github.com/boldsoftware/shelley). All development happens on the `molecula` branch of this repo (`molecula/shelley`).

# Shelley: a coding agent for exe.dev

Shelley is a mobile-friendly, web-based, multi-conversation, multi-modal,
multi-model, single-user coding agent built for but not exclusive to
[exe.dev](https://exe.dev/). It does not come with authorization or sandboxing:
bring your own.

*Mobile-friendly* because ideas can come any time.

*Web-based*, because terminal-based scroll back is punishment for shoplifting in some countries.

*Multi-modal* because screenshots, charts, and graphs are necessary, not to mention delightful.

*Multi-model* to benefit from all the innovation going on.

*Single-user* because it makes sense to bring the agent to the compute.

# Installation

## Pre-Built Binaries (macOS/Linux)

```bash
curl -Lo shelley "https://github.com/boldsoftware/shelley/releases/latest/download/shelley_$(uname -s | tr '[:upper:]' '[:lower:]')_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" && chmod +x shelley
```

The binaries are on the [releases page](https://github.com/boldsoftware/shelley/releases/latest).

## Homebrew (macOS)

```bash
brew install --cask boldsoftware/tap/shelley
```

## Build from Source

You'll need Go and Node.

```bash
git clone https://github.com/boldsoftware/shelley.git
cd shelley
make
```

# Releases

New releases are automatically created on every commit to `main`. Versions
follow the pattern `v0.N.9OCTAL` where N is the total commit count and 9OCTAL is the commit SHA encoded as octal (prefixed with 9).

# Architecture 

The technical stack is Go for the backend, SQLite for storage, and Typescript
and React for the UI. 

The data model is that Conversations have Messages, which might be from the
user, the model, the tools, or the harness. All of that is stored in the
database, and we use a SSE endpoint to keep the UI updated. 

# History

Shelley is partially based on our previous coding agent effort, [Sketch](https://github.com/boldsoftware/sketch). 

Unsurprisingly, much of Shelley is written by Shelley, Sketch, Claude Code, and Codex. 

# Shelley's Name

Shelley is so named because the main tool it uses is the shell, and I like
putting "-ey" at the end of words. It is also named after Percy Bysshe Shelley,
with an appropriately ironic nod at
"[Ozymandias](https://www.poetryfoundation.org/poems/46565/ozymandias)."
Shelley is a computer program, and, it's an it.

# Open source

Shelley is Apache licensed. We require a CLA for contributions.

# Running as a Service

## macOS (launchd)

This sets up Shelley as a user-level LaunchAgent that starts on login and
restarts automatically if it crashes.

### 1. Build

```bash
cd ~/shelley     # or wherever you cloned it
make install     # builds and installs to ~/.local/bin/shelley
```

### 2. Install the plist

Copy the template and fill in your paths:

```bash
sed -e "s|HOME_DIR|$HOME|g" \
    -e "s|YOUR_API_KEY|$ANTHROPIC_API_KEY|g" \
    com.shelley.serve.plist > ~/Library/LaunchAgents/com.shelley.serve.plist
```

### 3. Load the service

```bash
launchctl bootstrap user/$(id -u) ~/Library/LaunchAgents/com.shelley.serve.plist
```

Shelley is now running on http://localhost:9000.

### 4. Rebuild and restart

```bash
make install && launchctl kickstart -k user/$(id -u)/com.shelley.serve
```

### 5. Logs

```bash
tail -f ~/Library/Logs/shelley.log
```

### Uninstall

```bash
launchctl bootout user/$(id -u)/com.shelley.serve
rm ~/Library/LaunchAgents/com.shelley.serve.plist
```

## Linux (systemd)

A `shelley.service` unit file is included in the repo. See the file for details.

```bash
mkdir -p ~/.config/shelley
echo "ANTHROPIC_API_KEY=sk-ant-..." > ~/.config/shelley/env
cp shelley.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now shelley
```

Rebuild and restart:

```bash
make install && systemctl --user restart shelley
```

# Building Shelley

Run `make`. Run `make serve` to start Shelley locally.

## Dev Tricks

If you want to see how mobile looks, and you're on your home
network where you've got mDNS working fine, you can
run 

```
socat TCP-LISTEN:9001,fork TCP:localhost:9000
```

