---
name: pi-remote-mcp
description: Use pi-remote MCP tools to inspect, execute, or modify a pi-remote Gateway or Worker when the remote tools are available. Do not use for local workspace work or direct pi-remote-cli commands.
---

# pi-remote MCP

Use the `remote_*` MCP tools for work explicitly requested on a remote Gateway or
Worker. They never act on the current local workspace. Prefer these structured tools
over invoking `pi-remote-cli` through a shell when both are available.

## Connection and targets

The MCP server receives `PI_REMOTE_URL` and `PI_REMOTE_TOKEN` from its process
environment. Never place the Token in generated configuration, tool input, or
user-visible output.

An omitted `target` uses `PI_REMOTE_TARGET`, which defaults to `remote`: the machine
running the Gateway. Supply a Worker ID only when the requested machine is a
reverse-connected Worker. If the target is unspecified or its properties matter to
the task, use `remote_targets` first and verify its hostname, workspace root, OS,
shell profile, and online state.

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
