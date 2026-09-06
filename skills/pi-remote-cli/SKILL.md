---
name: pi-remote-cli
description: Use a pi-remote Gateway through its CLI to inspect or modify remote workspace files and run shell commands. Apply when work must be performed on a machine exposed by pi-remote rather than the Agent's local filesystem.
---

# pi-remote CLI

Use `pi-remote-cli` for remote workspace operations. Treat the selected remote target as a separate machine: local filesystem and shell tools do not see its files.

Before operating, require `PI_REMOTE_URL` and `PI_REMOTE_TOKEN`, or pass `--url` and `--token`. Use `PI_REMOTE_TARGET` or `--target` for a reverse-connected Worker; the default target is `local`, meaning the Gateway machine. Global flags must appear before the command.

Run `pi-remote-cli targets` when the target is not already known. Confirm the returned hostname, root, OS, shell profile, and online Worker ID before making target-specific assumptions.

Use the narrow command matching the operation:

- `read PATH`, `find QUERY`, `write PATH`, and `edit PATH` for files.
- `bash` (or `shell`) for shell execution. Provide multiline scripts on stdin and set `--timeout` when the default may be unsuitable. Use `--tty` when the remote program requires an interactive Unix PTY, and `--encoding` when its output is not UTF-8.
- `rpc TOOL` only for capabilities without a dedicated command.

Normal stdout is JSON so it can be inspected reliably. Use `--raw` with `read` or `bash` only when exact content is needed. `bash` propagates ordinary remote exit codes in both output modes; transport and RPC failures exit nonzero and emit a JSON error on stderr. Interactive `--tty` output is written directly to stdout.

The CLI prints copyable `PI_REMOTE_URL` and `PI_REMOTE_TOKEN` export commands to stderr at startup. Do not include that stderr output in logs that must not contain credentials.

Prefer stdin for multiline or quote-sensitive values:

```sh
printf '%s\n' 'go test ./...' | pi-remote-cli --target build-host bash --timeout 300
printf '%s' "$FILE_CONTENT" | pi-remote-cli --target build-host write src/example.txt
printf '%s' '[{"oldText":"before","newText":"after"}]' | pi-remote-cli --target build-host edit src/example.txt
```

Remote mutations and commands remain subject to the user's stated scope. Never retry a failed operation against `local` or a different Worker as an implicit fallback. Do not expose the Token in logs, generated files, or responses.
