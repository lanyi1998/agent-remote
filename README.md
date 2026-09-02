# pi-remote

[Chinese documentation](docs/README_ZH.md)

`pi-remote` exposes Pi's `read`, `bash`, `edit`, `write`, and remote file search capabilities through an authenticated RPC interface. The remote Worker actively establishes an outbound WebSocket connection, so the target machine does not need to expose an inbound port.

The source targets compatibility with Windows 7/2008.

## Features

`pi-remote` supports two connection modes:

- Forward connection: run `serve` on the target machine and let Pi connect directly to it.
- Reverse connection: run `serve` beside Pi and let the target machine connect outward as a `worker`; in this mode, neither Pi nor the target machine needs to expose an inbound port.

The Pi extension takes over `read`, `bash`, `edit`, `write`, file completion, and interactive `!` Bash commands.

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
```

The embedded runtime and Windows program support amd64 only.

## Windows Shell behavior

When the embedded BusyBox runtime is available, the Worker starts `busybox.exe sh -s` directly. Commands such as `ls`, `id`, `whoami`, `grep`, and `find` are handled by BusyBox without generating applet link files. This uses BusyBox's `ash` syntax rather than Bash syntax and never silently switches to CMD or PowerShell.

When available, timeouts and cancellations terminate the Windows Job Object, which also stops child processes. If the process cannot be added to a Job Object, the Worker falls back to terminating the Bash process directly.
