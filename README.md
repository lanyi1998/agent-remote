# pi-remote

pi-remote lets local agents such as Pi, Codex, and Claude operate remote homelab
machines or VPS instances. It does not rely on forward-connection services such as
RDP or SSH. A target machine can still be reached when it has no public IP address
and is protected by a firewall.

The source targets compatibility with Windows 7/2008.

## Features

`pi-remote` supports two connection modes:

- Forward connection: run `serve` on the target machine and let Pi connect to it.
- Reverse connection: run `serve` locally, and let the target machine connect out as
  a `worker`. This is suitable when the target machine is behind a firewall and
  cannot accept inbound connections.

The Pi extension takes over `read`, `bash`, `edit`, `write`, file completion, and
interactive `!` Bash commands.

For agents such as Codex, Claude Code, and OpenCode, both CLI and MCP integration are
available.

## Install the Pi extension

Copy the extension into Pi's auto-load directory:

```sh
mkdir -p ~/.pi/agent/extensions/pi-remote
cp pi-extension/index.ts ~/.pi/agent/extensions/pi-remote/index.ts
```

## Forward connection

The Gateway and Worker must use the same Token, which must be at least 8 characters
long.

Enter the Token in Pi with `/remote connect`; it is saved automatically after the
first successful connection.

Start the Gateway on the target machine:

```sh
./pi-remote-linux-amd64 serve --token 'replace-with-a-long-random-token'
```

![image-20260907164221111](https://blog-image.xemails.top/2026/4e76c4a2a5e5f3d5d7bd2492cd2e3c3b.webp)

Connect in Pi:

```text
/remote connect http://TARGET_IP:8787 replace-with-a-long-random-token My development machine
/remote status
```

![image-20260907172137321](https://blog-image.xemails.top/2026/251b9b74e650e16b1e0d1ca3e36e55ca.webp)

When the connection command does not specify `--worker`, it operates on the machine
running the Gateway. The optional trailing text is a display note. Select a
reverse-connected Worker like this:

```text
/remote connect http://VPS:8787 replace-with-a-long-random-token --worker office-linux My office machine
```

## Public VPS relay

When both Pi and the target machine are on private networks, use this topology:

```text
Pi  --HTTP-->  Public VPS Gateway  <--WebSocket--  Worker
```

Even with `http://` and `ws://`, RPC content and Worker messages are encrypted with
AES-256-GCM and authenticated with a timestamped, random-nonce HMAC. A passive
network observer cannot read the Token, commands, file contents, or execution
results. Because the VPS holds the shared Token, the VPS Gateway itself can still
decrypt the traffic.

Allow TCP port `8787` in the VPS cloud security group and system firewall.

Start the Gateway on the public VPS:

```sh
./pi-remote-linux-amd64 serve \
  --token 'replace-with-a-long-random-token' \
```

Start the Worker on the private-network target machine:

```sh
./pi-remote-linux-amd64 worker \
  --token 'replace-with-a-long-random-token' \
  --server ws://VPS_PUBLIC_IP:8787 \
  --id office-linux
```

The Worker actively establishes a persistent WebSocket connection and reconnects
automatically after disconnection. The target machine does not need a public IP,
port mapping, or an inbound firewall rule.

On the Pi machine:

```text
/remote connect http://VPS_PUBLIC_IP:8787 replace-with-a-long-random-token --worker office-linux
/remote status
```

The request path is:

```text
Pi -> HTTP -> VPS Gateway -> WebSocket -> office-linux Worker
```

Multiple Workers are supported, but every machine's `--id` must be unique:

```text
/remote office-linux
/remote home-linux
/remote win7-test
```

## Pi extension commands

```text
/remote                              # show remote subcommands
/remote status                       # show the current target
/remote list                         # list saved connections and their online status
/remote remove                       # remove a saved connection
/remote refresh                      # refresh Worker information
/remote off                          # disable the remote connection
/remote office-linux                 # select a Worker
/remote connect http://HOST:8787 TOKEN --worker ID [note]
```

## Health check

`/healthz` is the only endpoint intentionally left in plaintext:

```sh
curl http://HOST:8787/healthz
```

`/v1/targets` and `/v1/rpc` use the Token + HMAC + AES-256-GCM protocol and do not
accept a plaintext `Authorization: Bearer` header. Use the Pi extension or implement
the same secure protocol.

## Codex / Claude Code / OpenCode integration

`pi-remote-cli` can be used by any agent that supports command-line tools or standard
stdio MCP.

All integration methods use the following variables:

```sh
export PI_REMOTE_URL=http://HOST:8787
export PI_REMOTE_TOKEN=replace-with-a-long-random-token
export PI_REMOTE_TARGET=remote # optional; remote is the machine running the Gateway
```

### CLI

Use this when an Agent calls `pi-remote-cli` through a shell. First install the
repository's `/skills/pi-remote-cli/` into a Skill directory scanned by the Agent.

Then explicitly invoke the Skill in the Agent conversation to check the connection:

```text
$pi-remote-cli Run id on the remote machine and return the command result.
```

![image-20260907174706466](https://blog-image.xemails.top/2026/e19a373514cfc4dcf3f86dc4651a4609.webp)

### MCP

Add the MCP settings in Codex:

```toml
[mcp_servers.pi-remote]
command = "/opt/homebrew/bin/pi-remote-cli"
args = ["mcp"]
enabled = true
env_vars = ["PI_REMOTE_URL", "PI_REMOTE_TOKEN", "PI_REMOTE_TARGET"]
```

![image-20260907175607595](https://blog-image.xemails.top/2026/11ce0c39dd2a16cc4bb735ae3f044bf4.webp)

## Windows Shell behavior

Before building the Windows program, place the runtime file at:

```text
internal/runtimebundle/assets/
└── windows_amd64/
    └── bin/busybox.exe
```

Windows builds currently support amd64 only. Every file below `windows_amd64`, except
`_placeholder`, is embedded in `pi-remote.exe`.

When the embedded BusyBox runtime is available, the Worker starts
`busybox.exe sh -s` directly. Commands such as `ls`, `id`, `whoami`, `grep`, and
`find` are handled by BusyBox without creating applet link files. It uses BusyBox
`ash` syntax, not Bash syntax, and does not silently switch to CMD or PowerShell.

When available, timeouts and cancellation terminate the Windows Job Object and its
child processes. If the process cannot join a Job Object, the Worker falls back to
terminating the Bash process directly.
