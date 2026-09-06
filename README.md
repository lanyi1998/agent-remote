# pi-remote

[中文](docs/README_ZH.md)

`pi-remote` exposes Pi's `read`, `bash`, `edit`, `write`, and remote file search capabilities through an authenticated RPC interface. The remote Worker actively establishes an outbound WebSocket connection, so the target machine does not need to expose an inbound port.

The source targets compatibility with Windows 7/2008.

[![lanyi1998/pi-remote Git Commit History GitStock K-Line Chart](https://gitstock.org/lanyi1998/pi-remote/stock.svg)](https://gitstock.org/lanyi1998/pi-remote)

## Features

`pi-remote` supports two connection modes:

- Forward connection: run `serve` on the target machine and let Pi connect directly to it.
- Reverse connection: run `serve` beside Pi and let the target machine connect outward as a `worker`; in this mode, neither Pi nor the target machine needs to expose an inbound port.

The Pi extension takes over `read`, `bash`, `edit`, `write`, file completion, and interactive `!` Bash commands.

For other agents and scripts, the repository also provides a standalone `pi-remote-cli` client with JSON output.

## Install the Pi extension

Copy the extension to Pi's auto-load directory:

```sh
mkdir -p ~/.pi/agent/extensions/pi-remote
cp pi-extension/index.ts ~/.pi/agent/extensions/pi-remote/index.ts
cp pi-extension/README.md ~/.pi/agent/extensions/pi-remote/README.md
```

Restart Pi and the extension will load automatically. Gateway and Worker must use the same Token, and the Token must be at least 8 characters long. Enter the Token in Pi with `/remote connect`; it will be saved automatically after the first successful connection.

## Forward connection

Start the Gateway on the target Linux machine:

```sh
./pi-remote-linux-amd64 serve \
  --token 'replace-with-a-long-random-token'
```

Connect from Pi:

```text
/remote connect http://TARGET_IP:8787 replace-with-a-long-random-token My development machine
/remote status
```

When the connection command does not specify `--worker`, it operates on the machine running the Gateway. The optional trailing text is a display note. Select a reverse-connected Worker with:

```text
/remote connect http://VPS:8787 replace-with-a-long-random-token --worker office-linux My office machine
```

## Public VPS relay without TLS

When both Pi and the target machine are on private networks, use the following topology:

```text
Pi  --HTTP-->  Public VPS Gateway  <--WebSocket--  Worker
```

Even when using `http://` and `ws://`, RPC content and Worker messages are encrypted with AES-256-GCM and authenticated with a timestamped, random-nonce HMAC. A passive network sniffer cannot read the Token, commands, file contents, or execution results. Because the VPS holds the shared Token, the VPS Gateway itself can still decrypt the traffic.

Allow TCP port `8787` in the VPS cloud security group and system firewall.

Start the Gateway on the public VPS:

```sh
./pi-remote-linux-amd64 serve \
  --token 'replace-with-a-long-random-token'
```

Start the Worker on the target machine in the private network:

```sh
./pi-remote-linux-amd64 worker \
  --token 'replace-with-a-long-random-token' \
  --server ws://VPS_PUBLIC_IP:8787 \
  --id office-linux
```

The Worker actively establishes a persistent WebSocket connection and reconnects automatically after a disconnection. The target machine does not need a public IP address, port mapping, or an inbound firewall rule.

On the Pi machine:

```text
/remote connect http://VPS_PUBLIC_IP:8787 replace-with-a-long-random-token --worker office-linux
/remote status
```

The request path is:

```text
Pi -> HTTP -> VPS Gateway -> WebSocket -> office-linux Worker
```

Multiple Workers are supported, but each machine's `--id` must be unique:

```text
/remote office-linux
/remote home-linux
/remote win7-test
```

## Optional TLS configuration

Application-layer encryption already covers the no-TLS scenario. If additional transport-layer protection is needed, the Go Gateway can load a certificate directly:

```sh
./pi-remote-linux-amd64 serve \
  --token 'replace-with-a-long-random-token' \
  --listen 0.0.0.0:8787 \
  --tls-cert /path/to/fullchain.pem \
  --tls-key /path/to/privkey.pem \
  --root /tmp
```

When TLS is enabled, Pi uses `https://HOST:8787` and the Worker uses `wss://HOST:8787`.

## Switch targets in Pi

```text
/remote                              # show remote subcommands
/remote status                       # show the current target
/remote list                         # list saved connections and online status
/remote remove                       # remove a saved connection
/remote refresh                      # refresh Worker information
/remote off                          # use Pi's local tools
/remote office-linux                 # select a Worker
/remote connect http://HOST:8787 TOKEN --worker ID [note]
```

Switching waits for the current Agent turn to become idle. The selected target's metadata is saved to the Pi session and injected into subsequent model context, including the target ID, connection direction, online status, hostname, operating system, CPU architecture, workspace directory, and shell type.

Connection information is saved to `~/.pi/agent/remote.json`, including the Gateway URL, target, note, and shared Token. The file permissions are `0600`; the saved Token can also be overridden with `--pi-remote-token`.

## Health check

The only endpoint intentionally left in plaintext is `/healthz`:

```sh
curl http://HOST:8787/healthz
```

`/v1/targets` and `/v1/rpc` use the Token + HMAC + AES-256-GCM protocol and do not accept a plaintext `Authorization: Bearer` header. Use the Pi extension or implement the same secure protocol.

## CLI for other agents

Build and install the client:

```sh
go build -o bin/pi-remote-cli ./cmd/pi-remote-cli
install -m 0755 bin/pi-remote-cli /usr/local/bin/pi-remote-cli
```

The second command is optional; alternatively, invoke `./bin/pi-remote-cli` directly.

Configure it with environment variables:

```sh
export PI_REMOTE_URL=http://HOST:8787
export PI_REMOTE_TOKEN=replace-with-a-long-random-token
export PI_REMOTE_TARGET=office-linux # optional; defaults to local
```

Or put connection flags before the command. Command-line flags override environment variables:

```sh
pi-remote-cli --url http://HOST:8787 --token TOKEN --target office-linux targets
pi-remote-cli read --offset 1 --limit 200 README.md
pi-remote-cli find --max-results 50 internal
pi-remote-cli bash --timeout 300 'go test ./...'
pi-remote-cli bash --tty --timeout 300 --encoding utf-8 './tools/setup/startup-linux.sh'
printf '%s' 'new content' | pi-remote-cli write path/to/file.txt
printf '%s' '[{"oldText":"before","newText":"after"}]' | pi-remote-cli edit path/to/file.txt
printf '%s' '{"path":"README.md"}' | pi-remote-cli rpc read
```

Global flags must appear before the subcommand:

```text
--url URL              Gateway URL; defaults to PI_REMOTE_URL
--token TOKEN          shared Token; defaults to PI_REMOTE_TOKEN
--target ID            local or Worker ID; defaults to PI_REMOTE_TARGET or local
--raw                  print raw content for read and bash
--request-timeout D    whole-request timeout, for example 30s or 2m
```

Available commands:

```text
targets
    List Gateway and connected Worker metadata.

read [--offset N] [--limit N] PATH
    Read a remote text or binary file. Binary data is base64-encoded in JSON mode.

find [--max-results N] [QUERY]
    Find files and directories below the configured remote workspace root.

bash [--timeout SEC] [--tty] [--encoding NAME] [COMMAND...]
shell [--timeout SEC] [--tty] [--encoding NAME] [COMMAND...]
    Execute a remote shell command. When COMMAND is omitted, read the script from stdin.
    --tty connects the local stdin/stdout to a remote PTY for interactive programs.
    --encoding selects the remote output encoding, such as utf-8, gb18030, gbk, big5, or shift-jis.

write [--content TEXT | --content-file FILE] PATH
    Replace a remote file. When neither content option is present, read content from stdin.

edit [--edits JSON | --edits-file FILE] PATH
    Apply exact replacements. The JSON value is an array of oldText/newText objects.
    When neither edits option is present, read the JSON array from stdin.

rpc [--input JSON | --input-file FILE] TOOL
    Invoke a tool directly. When neither input option is present, read JSON from stdin.
```

For multiline scripts, stdin avoids shell quoting problems:

```sh
pi-remote-cli --target office-linux bash --timeout 300 <<'SCRIPT'
set -e
go test ./...
go build ./...
SCRIPT
```

An edit request looks like this:

```sh
pi-remote-cli edit README.md <<'JSON'
[
  {
    "oldText": "old text",
    "newText": "new text"
  }
]
JSON
```

Normal stdout is formatted JSON, which is suitable for agent and script consumption. Add the global `--raw` flag before the command to print file or shell content directly:

```sh
pi-remote-cli --raw read README.md
pi-remote-cli --raw bash 'uname -a'
```

Use `--tty` when the remote command needs a real terminal, for example an interactive
installer. The terminal session forwards keyboard input and remote output directly;
PTY sessions are currently supported on Unix-like targets.

When the Gateway starts with `serve`, it prints reusable `PI_REMOTE_URL` and
`PI_REMOTE_TOKEN` export commands plus a Pi `/remote connect URL TOKEN` command.
A wildcard listen address is replaced with a reachable local IPv4 address, and TLS
deployments use an `https://` URL.

`bash` returns the remote command's exit status when it is in the `1-255` range, in both JSON and raw output modes. Transport and RPC failures return exit status `1` and a JSON error on stderr. The bundled Agent Skill is at [`skills/pi-remote-cli/SKILL.md`](skills/pi-remote-cli/SKILL.md).

Run `pi-remote-cli --help` for the built-in command summary. The Token is used locally to authenticate and encrypt requests; avoid placing it in shell history on shared machines and prefer `PI_REMOTE_TOKEN` in that situation.

## Runtime files

Before building the Windows program, place the runtime files under:

```text
internal/runtimebundle/assets/
└── windows_amd64/
    └── bin/busybox.exe
```

Windows builds currently support amd64 only. All files under `windows_amd64` except `_placeholder` are embedded in `pi-remote.exe`.

On the first Shell command executed on Windows, the embedded files are extracted to a content-addressed directory under `%LOCALAPPDATA%/PiRemote/runtime`. Every file is verified against the embedded content. Agent scripts are sent to BusyBox `sh` over stdin and are not written to a temporary script file.

For development, skip embedding and specify an existing runtime:

```sh
pi-remote worker \
  --token 'replace-with-a-long-random-token' \
  --server ws://gateway.example \
  --root C:/work \
  --busybox-path C:/tools/busybox.exe
```

## Build

Use Go 1.20.14 for the Windows 7 program:

```sh
go mod download
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o bin/pi-remote-windows-amd64.exe ./cmd/pi-remote
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o bin/pi-remote-cli-windows-amd64.exe ./cmd/pi-remote-cli
```

The embedded runtime and Windows program support amd64 only.

## Windows Shell behavior

When the embedded BusyBox runtime is available, the Worker starts `busybox.exe sh -s` directly. Commands such as `ls`, `id`, `whoami`, `grep`, and `find` are handled by BusyBox without generating applet link files. This uses BusyBox's `ash` syntax rather than Bash syntax and never silently switches to CMD or PowerShell.

When available, timeouts and cancellations terminate the Windows Job Object, which also stops child processes. If the process cannot be added to a Job Object, the Worker falls back to terminating the Bash process directly.
