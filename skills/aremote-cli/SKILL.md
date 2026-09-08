---
name: aremote
description: Operate an agent-remote Gateway or Worker through the aremote command when a user asks to inspect, execute, or modify a remote machine from a shell or script. Do not use for MCP tool calls or the current local workspace.
---

# agent-remote CLI

Use `aremote` for work explicitly requested on a remote Gateway or Worker.
It operates on a different machine from the local workspace; never substitute local
shell or filesystem commands for a remote request.

## Connection and targets

Run `aremote connect URL TOKEN [--worker ID] [NOTE...]` before operating on a
remote machine. It verifies the target and saves the Token and target selection in
`~/.agent-remote/config.json`; never expose the Token in output, logs, or generated
files.

Run `aremote status` to verify the active target. `aremote list` shows saved
targets and online states; in an interactive terminal, enter a listed number to
activate it. To select a Worker that has not been saved, run `aremote connect URL
TOKEN --worker ID [NOTE...]`.
Use `aremote rename NOTE...` to update the active target's note without reconnecting.

For a one-off operation on another saved machine, pass its ID from `aremote list`
without changing the active target: `aremote --target TARGET_ID bash
COMMAND`. This is the preferred approach when multiple agents work on different
machines concurrently.

## Choose commands deliberately

- Use `read` and `find` to inspect remote files.
- Use `write` and `edit` only for requested remote file changes.
- Use `bash` for non-interactive commands; use `bash --tty` only when a real remote
  terminal is required.
- Use `rpc TOOL` only if no dedicated command supports the operation.

Normal output is JSON. Add `--raw` only when exact file or command output is needed.
`bash`, `write`, `edit`, and `rpc` accept stdin when their inline or file input
option is omitted; prefer stdin for multiline or quote-sensitive values.

```sh
printf '%s\n' 'go test ./...' | aremote bash --timeout 300
printf '%s' "$FILE_CONTENT" | aremote write src/example.txt
printf '%s' '[{"oldText":"before","newText":"after"}]' | aremote edit src/example.txt
```

Remote shell exit codes from 1 through 255 are preserved. Transport and RPC failures
return a nonzero status and a JSON error on stderr. Do not silently retry a failed
operation against a different target.

For non-UTF-8 command output, pass `--encoding`, such as `gb18030` or `big5`.
