---
name: agent-remote-mcp
description: Use agent-remote MCP tools to inspect, execute, or modify an agent-remote Gateway or Worker when the remote tools are available. Do not use for local workspace work or direct aremote commands.
---

# agent-remote MCP

Use the `remote_*` MCP tools for work explicitly requested on a remote Gateway or
Worker. They never act on the current local workspace. Prefer these structured tools
over invoking `aremote` through a shell when both are available.

## Connection and targets

Before starting MCP, use `aremote connect URL TOKEN [--worker ID] [NOTE...]` to save
and select the target in `~/.agent-remote/config.json`. The MCP server uses that
active connection; never place the Token in generated configuration, tool input, or
user-visible output.

If target properties matter to the task, use `remote_targets` first and verify its
hostname, workspace root, OS, shell profile, and online state.

## Tool routing

- `remote_read`: read a remote text or binary file; use `offset` and `limit` for a
  bounded text range.
- `remote_find`: locate remote files and directories.
- `remote_bash`: run a non-interactive remote shell command; provide `timeout` and
  `encoding` when needed.
- `remote_write`: replace an entire UTF-8 remote file when that overwrite is
  explicitly requested.
- `remote_edit`: make exact, targeted replacements; prefer it over rewriting a file
  when the change is localized.

`remote_bash` cannot provide an interactive terminal. Do not pretend it can; an
interactive session requires the separate CLI `bash --tty` workflow.

Keep operations within the user's requested target and scope. If a tool call fails,
report the failure or investigate that same target; do not fall back to a different
Worker or the local machine.
