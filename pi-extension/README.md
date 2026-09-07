# Pi extension

This extension overrides Pi's `read`, `bash`, `edit`, and `write` tools, routes
interactive `!` Bash commands, and routes `@` file completion and file loading
to the active execution target. It supports both connection directions.

## Load it

```sh
pi -e /absolute/path/to/pi-remote/pi-extension/index.ts
```

Pi 侧的令牌通过 `/remote connect` 输入，不从环境变量读取。
使用 `http://` 或 `ws://` 时，扩展会用 Token 进行 HMAC 认证，并使用
AES-256-GCM 加密请求和响应；原始 Token 不会放入网络请求。

Install it permanently with Pi's extension installer if desired:

```sh
pi install /absolute/path/to/pi-remote/pi-extension/index.ts
```

## Forward connection

The target machine listens for the Gateway API:

```powershell
pi-remote.exe serve --listen 0.0.0.0:8787 --root C:/work
```

Pi connects directly to that machine; the target defaults to the Gateway machine:

```sh
pi -e ./pi-extension/index.ts
```

Then connect inside Pi:

```text
/remote connect http://remote-machine.example:8787 YOUR_TOKEN my-development-machine
```

Gateway traffic is authenticated and encrypted with AES-256-GCM. Firewall
allowlists are still recommended when exposing the forward listener.

## Reverse connection

Run the Gateway beside Pi:

```sh
pi-remote serve --listen 127.0.0.1:8787 --root /local/work
```

The remote machine connects outward:

```powershell
pi-remote.exe worker `
  --server ws://gateway.example `
  --id win7-build-01 `
  --root C:/work
```

Start Pi against that Gateway and choose the Worker ID:

```sh
pi -e ./pi-extension/index.ts
```

Then connect inside Pi:

```text
/remote connect http://127.0.0.1:8787 YOUR_TOKEN --worker win7-build-01 windows-build-machine
```

## Switching targets

Use `/remote` to list the available subcommands. Use `/remote list` to inspect
all saved connections and their online status; selecting one reuses it.
Explicit forms are:

```text
/remote status
/remote off                         # Pi's own local machine
/remote win7-build-01               # reverse-connected Worker
/remote refresh
/remote list                         # list saved connections and online status
/remote remove                       # remove a saved connection
/remote connect http://host:8787 YOUR_TOKEN my-development-machine
/remote connect http://host:8787 YOUR_TOKEN --worker win7-build-01 windows-build-machine
/remote note my-development-machine
```

The optional trailing text is a user-facing note for the active remote target.
A connection without `--worker` defaults to the Gateway machine. Notes are
stored per saved connection; when omitted, Pi generates a random note automatically.

Successful connections are saved in `~/.pi/agent/remote.json`, including the
Gateway URL, target, note, and shared token. `/remote list` checks every saved
connection and lets you select an online one. The file is created with mode
`0600`; the token can still be overridden by `--pi-remote-token`. Pi starts with
its local tools; use `/remote connect` or select a connection from `/remote list`
to enter remote mode. Starting `/new` within the same Pi process keeps the
current remote target. Use `/remote remove` to delete a saved connection;
removing the active connection also switches Pi back to its local tools.

Switching waits until the current agent turn is idle. The extension then:

1. persists the selected target in the Pi session;
2. updates the status bar;
3. appends a target-change message to model context;
4. injects current target metadata into every subsequent system prompt.

The model receives target ID, direction, online state, hostname, OS,
architecture, workspace root, and shell profile. A failed remote call never
falls back to the Pi host.

The Gateway URL and initial target can also come from environment variables:

```text
PI_REMOTE_URL
PI_REMOTE_TARGET
```
