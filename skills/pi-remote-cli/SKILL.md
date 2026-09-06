---
name: pi-remote-cli
description: Use a pi-remote Gateway or its MCP tools for work explicitly requested on a remote machine, Gateway, or Worker. Do not use for the current local workspace.
---

# pi-remote CLI

Use `pi-remote-cli` for work on the selected Gateway or Worker. It is a different
machine from the local workspace: local shell and filesystem tools do not operate on
its files.

## Connect

Start the Gateway with `pi-remote serve`. Its startup output provides both connection
forms:

```text
# Local CLI
export PI_REMOTE_URL='http://HOST:8787'
export PI_REMOTE_TOKEN='TOKEN'

# Pi
/remote connect http://HOST:8787 TOKEN
```

Copy the two `export` commands into the local shell before using the CLI. Alternatively,
pass `--url` and `--token`. Do not expose the Token in logs, generated files, or
responses.

Use `PI_REMOTE_TARGET` or `--target` to select a reverse-connected Worker. The default
is `local`, which means the Gateway machine. Global flags must come before the command.
Run `pi-remote-cli targets` when the target is not already known, and verify its
hostname, workspace root, OS, shell profile, and online status before assuming details
about it.

## Choose the narrow command

- Use `read`, `find`, `write`, and `edit` for remote filesystem operations.
- Use `bash` or `shell` for remote command execution.
- Use `rpc TOOL` only when no dedicated command is available.

Normal output is JSON. Add `--raw` before `read` or `bash` only when the exact remote
content is required. Remote shell exit statuses from 1 to 255 are preserved; transport
and RPC failures return a nonzero status and write a JSON error to stderr.

Use stdin for multiline scripts and quote-sensitive write, edit, or RPC input:

```sh
printf '%s\n' 'go test ./...' | pi-remote-cli --target build-host bash --timeout 300
printf '%s' "$FILE_CONTENT" | pi-remote-cli --target build-host write src/example.txt
printf '%s' '[{"oldText":"before","newText":"after"}]' | pi-remote-cli --target build-host edit src/example.txt
```

## Interactive commands and encoding

Use `bash --tty` when a remote Unix-like program needs a real terminal, such as an
installer or an interactive shell. `--pty` is an alias. In this mode local stdin and
stdout are connected directly to the remote PTY, including terminal-size updates; it
does not produce the normal JSON result.

Use `--encoding` when remote command output is not UTF-8. Supported examples include
`gbk`, `gb18030`, `big5`, `shift-jis`, and `windows-1252`:

```sh
pi-remote-cli --target build-host bash --tty --timeout 300 --encoding gb18030 './install.sh'
```

Remote mutations and shell commands remain limited to the user's stated scope. Do not
retry a failed operation against `local` or a different Worker as an implicit fallback.

## MCP mode

For an MCP-capable Agent, configure the same executable with the `mcp` argument. It
uses standard stdio JSON-RPC and reads `PI_REMOTE_URL`, `PI_REMOTE_TOKEN`, and optional
`PI_REMOTE_TARGET` from the process environment:

```json
{
  "command": "/path/to/pi-remote-cli",
  "args": ["mcp"],
  "env": {
    "PI_REMOTE_URL": "http://HOST:8787",
    "PI_REMOTE_TOKEN": "TOKEN"
  }
}
```

MCP exposes `remote_targets`, `remote_read`, `remote_find`, `remote_bash`,
`remote_write`, and `remote_edit` as structured tools. Use those tools instead of
shelling out to the CLI when the host supports MCP. `remote_bash` is non-interactive;
use CLI `bash --tty` only when an actual terminal is needed. Never write diagnostic
text to MCP stdout or place the Token in generated configuration files. The server can
start before the connection variables are supplied, but remote tool calls require both
`PI_REMOTE_URL` and `PI_REMOTE_TOKEN` in the MCP process environment.

When the user explicitly asks to execute, inspect, read, or modify something on a
remote machine, use the corresponding `remote_*` MCP tool before inspecting the local
workspace for SSH settings, host files, or scripts. Do not substitute a local shell
command for a remote request. If the target is not named, use `remote_targets` to
identify the configured Gateway and Workers, then continue with the requested remote
operation.
