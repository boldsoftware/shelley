---
name: schedule
description: Schedules future tasks or wakes the current conversation after a delay. Use for reminders, recurring tasks, or resuming work later.
---

## Scheduled tasks

Use systemd user timer units. Unless the user explicitly asked to schedule something, have the user confirm.

If the task is obvious and straightforward, such as a bash command, you might schedule just the command. Otherwise, schedule a future Shelley conversation by calling `shelley client chat`.

Name shelley-calling units `shelley-<name>.{service,timer}`. The service ExecStart should invoke:

```
shelley client chat -p '<prompt>' -cwd '<working_directory>'
```

Each timer firing creates a new conversation. To wake up your own conversation instead, add `-c $SHELLEY_CONVERSATION_ID`.

The prompt baked into the service unit should concisely convey the overarching goals and context from the user, preferably in their own words, as well as the specific task being achieved by this scheduled invocation. The prompt must always include the originating conversation ID (from `$SHELLEY_CONVERSATION_ID`), so that new agent can refer to the originating conversation for additional context if needed.

You are responsible for ensuring that one-shot units will be cleaned up.

## Resume this conversation after a delay

For a one-off wait within ongoing work, detach a tmux session that sleeps then
messages the current conversation. Use double quotes so the invoking shell
expands `$SHELLEY_CONVERSATION_ID`; tmux's server environment may be stale.

```bash
tmux new-session -d "sleep 3600 && shelley client chat -c $SHELLEY_CONVERSATION_ID -p 'Resume: <what next>'"
```
