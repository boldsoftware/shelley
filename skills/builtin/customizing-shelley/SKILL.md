---
name: customizing-shelley
description: Use when the user wants to change Shelley itself — its code, UI, tools, or behavior beyond what hooks allow — or asks to rebase/upgrade a customized Shelley build.
---

Shelley is open source: https://github.com/boldsoftware/shelley. You can check it out, modify it, build it, and run it — including replacing the very binary serving this conversation.

For small behavior tweaks (system prompt, new-conversation defaults), prefer the `shelley-hooks` skill; it needs no rebuild. Use this skill when the change requires modifying Shelley's source.

## Checkout

The canonical checkout lives at `~/.config/shelley/shelley-customization`. Create it if missing:

```
git clone https://github.com/boldsoftware/shelley ~/.config/shelley/shelley-customization
cd ~/.config/shelley/shelley-customization
git checkout -b custom
```

If it already exists, work on its `custom` branch. Keep all customizations as clean, well-described commits on `custom` — the upgrade flow rebases this branch, so avoid uncommitted work.

Avoid altering existing database schema (`db/schema/`) or existing migrations: mainline adds migrations constantly, and edits to shared tables are the likeliest source of painful rebase conflicts (and can diverge the user's database from what mainline migrations expect). If a customization needs to store data, add a new migration creating a *new* table instead of adding columns to existing ones.

If the running Shelley is already customized (`shelley version` reports `"customized": true`), the checkout is the source of truth for what's deployed.

## Build

```
cd ~/.config/shelley/shelley-customization
make build-custom
```

This builds `bin/shelley` stamped as a customized build: the version dialog will show that the build has diverged from mainline (rather than merely being outdated) and will offer a rebase-based upgrade instead of a binary self-update. Do not build customized binaries with plain `make build` — an unstamped binary will wrongly offer binary self-upgrades that would silently discard the customizations.

Requires Go, pnpm, and make, and a full (non-shallow) clone with tags. Run `go test` on the packages you touched before offering to install.

## Trying it out and installing

After a successful build, offer the user both options:

1. **Run it off to the side** (safe preview): start the new binary on a spare port with a separate database and give the user the URL to try:

   ```
   ~/.config/shelley/shelley-customization/bin/shelley -db /tmp/shelley-custom-preview.db serve -port 8010
   ```

   Run it in tmux so it survives the turn. On exe.dev VMs the user reaches it at `https://<host>.exe.xyz:8010/`.

2. **Install over the running binary and restart**: find where the running Shelley lives — don't guess:

   ```
   curl -s "$SHELLEY_URL/version-check" | jq -r .executable_path
   ```

   (`SHELLEY_URL` is set in your environment; typically the path is `/usr/local/bin/shelley`.) You cannot copy over a running binary ("Text file busy"), so install side-by-side and rename:

   ```
   DEST=$(curl -s "$SHELLEY_URL/version-check" | jq -r .executable_path)
   sudo cp ~/.config/shelley/shelley-customization/bin/shelley "$DEST.new"
   sudo chown --reference="$DEST" "$DEST.new"
   sudo chmod --reference="$DEST" "$DEST.new"
   sudo mv "$DEST.new" "$DEST"
   ```

   You are likely running *inside* the server you're replacing. When the version check reports `running_under_systemd: true`, install the binary, then make this the final tool action of the turn:

   ```
   curl -fsS -X POST -H 'X-Shelley-Request: 1' "$SHELLEY_URL/exit?resume=true"
   ```

   The endpoint marks every in-flight conversation for one-shot continuation, exits the old process, and lets systemd start the newly installed binary. Shelley will continue this installation conversation after the restart, so do not send a final reply first and do not separately run `systemctl restart shelley`.

   If Shelley is not under systemd or another restart-on-exit supervisor, do not call `/exit`; tell the user to restart it themselves.

Never install without the user explicitly choosing option 2.

## Upgrading a customized build (rebase flow)

When Shelley is customized, the version dialog's upgrade button starts a conversation asking you to rebase the customizations onto the latest mainline. To do that:

```
cd ~/.config/shelley/shelley-customization
git fetch origin main --tags
git rebase origin/main custom
```

Resolve conflicts thoughtfully — you have the user's customization commits and their messages for context; ask the user when intent is unclear. Then rebuild with `make build-custom`, run relevant tests, and offer the same run-aside/install choice as above.

If the user instead wants to abandon their customizations and return to mainline releases, download the latest release binary for this platform (URLs in https://boldsoftware.github.io/shelley/release.json), install it the same side-by-side way as above, and leave the checkout in place.
