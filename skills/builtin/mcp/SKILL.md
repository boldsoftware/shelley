---
name: mcp
description: Use MCP (Model Context Protocol) servers from bash with `shelley mcp`. Use when the user mentions MCP, asks to add or connect an MCP server, or a task needs an external service that may be reachable through a registered MCP server.
---

MCP servers registered with Shelley aren't model tools: use them by running `shelley mcp` from bash (`shelley mcp -h` for all commands).

```bash
shelley mcp list                    # servers
shelley mcp list SERVER             # its instructions and tools as TypeScript-style signatures
shelley mcp list SERVER.TOOL -schema  # one tool with its JSON schemas
shelley mcp search issue label     # tools matching every term, across all servers
shelley mcp search SERVER issue     # ...limited to one server
shelley mcp call linear.search_issues query='login bug' limit=5 'states=["open"]'
```

To find a tool, prefer `shelley mcp search TERM...` over grepping `list`: it
matches the terms against each tool's name, description and parameters and
prints the full documentation of every matching tool, grouped by server.

Read a server's instructions and signatures before first calling its tools. Some tools have an interactive view (an MCP App): calling one shows the result in it, in the conversation, where the user can act on it, so don't repeat what it shows. `KEY=VALUE` values are taken as is for string parameters and as JSON otherwise, so quote JSON for the shell. Images, audio and binary resources are saved to temporary files (the output says where; look at images with read_image). If the tool reports an error, it is printed to stderr and the exit status is 1. A call that timed out may still have run.

Compose calls like any shell command; keep output small with jq:

```bash
shelley mcp call SERVER.TOOL -json | jq '.structuredContent.items[] | {id, title}'
jq -n --arg q "$query" '{query: $q, limit: 5}' | shelley mcp call SERVER.search -   # arguments as JSON on stdin
for id in 1 2 3; do shelley mcp call SERVER.get_item id="$id"; done
shelley mcp call A.list_rows -json > /tmp/rows.json
jq -c '.structuredContent.rows[] | {title: .name}' /tmp/rows.json |
  while read -r args; do echo "$args" | shelley mcp call B.create_item -; done
```

## Log in

When a command reports an authentication problem, give the user its action as a clickable Markdown link, not a bare path or code span. For example, `[Sign in](/mcp/login/SERVER)` for direct servers or `[Manage integrations](https://exe.dev/integrations)` for integration-managed authentication. Preserve the destination the command supplies. Wait for the user to finish, then retry. Never open or fetch the link yourself: it's for the user's browser, and only they can log in. `shelley mcp auth SERVER` shows a direct server's login state and link. Never ask for or invent tokens for servers that log in this way.

## Register

```bash
shelley mcp add NAME -d 'what it is for' https://example.com/mcp   # logs in with OAuth if needed
shelley mcp add NAME -H 'Authorization: Bearer TOKEN' https://example.com/mcp
shelley mcp rm NAME
```

For servers that take an API key, ask the user for it. Shelley only connects to Streamable HTTP servers, not to stdio ones (local commands such as `npx ...`).

Editing a server's URL to tweak its path or query (a common way to pick which tools it exposes) keeps an existing OAuth login; only changing the scheme or host, or giving it an `Authorization` header, drops it.

## Sessions

Each conversation has its own session with each server, so server state (open pages, selections) lasts across calls; forks and subagents start fresh. `shelley mcp restart SERVER` starts this conversation's session over, losing its state.
