---
name: aremote
description: Operate an agent-remote Gateway or Worker through the aremote command when a user asks to inspect, execute, or modify a remote machine from a shell or script. Do not use for MCP tool calls or the current local workspace.
---

# agent-remote CLI

Use `aremote` for work explicitly requested on a remote Gateway or Worker.
It operates on a different machine from the local workspace; never substitute local
shell or filesystem commands for a remote request.

## Connection and targets

The CLI reads `AGENT_REMOTE_URL` and `AGENT_REMOTE_TOKEN`, or accepts `--url` and
`--token` before the subcommand. Keep the Token out of command output, logs, and
generated files.

`AGENT_REMOTE_TARGET` or `--target` selects the execution target. It defaults to
`remote`, which is the machine running the Gateway. A Worker ID selects a
reverse-connected Worker. If the requested target is not known, run
`aremote targets` and verify its hostname, workspace root, OS, shell profile,
and online state before assuming details.

## Choose commands deliberately

- Use `read` and `find` to inspect remote files.
- Use `write` and `edit` only for requested remote file changes.
- Use `bash` for non-interactive commands; use `bash --tty` only when a real remote
  terminal is required.
- Use `rpc TOOL` only if no dedicated command supports the operation.

Global flags must precede the command. Normal output is JSON. Add `--raw` only when
exact file or command output is needed. `bash`, `write`, `edit`, and `rpc` accept
stdin when their inline or file input option is omitted; prefer stdin for multiline
or quote-sensitive values.

```sh
printf '%s\n' 'go test ./...' | aremote --target build-host bash --timeout 300
printf '%s' "$FILE_CONTENT" | aremote --target build-host write src/example.txt
printf '%s' '[{"oldText":"before","newText":"after"}]' | aremote --target build-host edit src/example.txt
```

Remote shell exit codes from 1 through 255 are preserved. Transport and RPC failures
return a nonzero status and a JSON error on stderr. Do not silently retry a failed
operation against a different target.

For non-UTF-8 command output, pass `--encoding`, such as `gb18030` or `big5`.
