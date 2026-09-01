# pi-remote

`pi-remote` exposes Pi's `read`, `bash`, `edit`, `write`, and remote file search operations over an
authenticated RPC endpoint. A remote worker makes the outbound WebSocket
connection, so the remote machine does not need an inbound port.

The source targets Go 1.20 syntax. Build Windows 7 artifacts with the actual Go
1.20.14 toolchain; setting `go 1.20` in `go.mod` alone does not make a binary
built by a newer toolchain compatible with Windows 7.

## 使用方法

`pi-remote` 支持两种工作方式：

- 正向连接：目标机器运行 `serve`，Pi 直接连接目标机器。
- 反向连接：公网 VPS 运行 `serve`，内网目标机器运行
  `worker` 主动连接 VPS。Pi 也主动连接 VPS，两台内网机器都不需要
  开放入站端口。

Pi 扩展会接管 `read`、`bash`、`edit`、`write`、`@` 文件补全和交互式
`!` Bash 命令。远程调用失败时不会静默回退到 Pi 本机。

### 1. 安装 Pi 扩展

将扩展复制到 Pi 的自动加载目录：

```sh
mkdir -p ~/.pi/agent/extensions/pi-remote
cp pi-extension/index.ts ~/.pi/agent/extensions/pi-remote/index.ts
cp pi-extension/README.md ~/.pi/agent/extensions/pi-remote/README.md
```

重新启动 Pi 后，扩展会自动加载。Gateway 和 Worker 必须使用相同的令牌，
令牌长度至少为 16 个字符；Pi 侧在 `/remote connect` 命令中输入令牌，
首次连接成功后会自动保存。

### 2. Linux 程序

已编译的 x86-64 Linux 程序默认位于：

```text
dist/pi-remote-linux-amd64
```

上传到 Linux 后赋予执行权限：

```sh
chmod +x pi-remote-linux-amd64
```

Linux 版不需要释放内嵌运行时，会直接使用 `/bin/bash` 或
`/usr/bin/bash`。如果 Bash 位于其他位置，可以增加
`--bash-path /path/to/bash`。

### 3. 正向连接

在目标 Linux 机器上启动 Gateway：

```sh
export PI_REMOTE_TOKEN='1234567890123456'

./pi-remote-linux-amd64 serve \
  --listen 0.0.0.0:8787 \
  --root ./
```

在 Pi 机器上启动：

```sh
export PI_REMOTE_URL='http://TARGET_IP:8787'
pi
```

也可以在 Pi 内动态连接：

```text
/remote connect http://TARGET_IP:8787 1234567890123456 我的开发机
/remote status
```

连接命令不指定 Worker 时默认操作 Gateway 所在机器，末尾文本是可自定义的
远程机器备注；不填写时 Pi 会自动生成一个随机备注。反向连接 Worker 时
使用 `/remote connect http://VPS:8787 1234567890123456 --worker office-linux 我的办公机`。
也可以在连接后执行 `/remote note 我的开发机` 修改备注。

### 4. 使用公网 VPS 中转（无 TLS）

适用于 Pi 机器和目标机器都处于内网的情况：

```text
Pi 机器  --HTTP-->  公网 VPS  <--WebSocket--  内网 Worker
```

只需在 VPS 的云安全组和系统防火墙中放行 TCP `8787`。

在公网 VPS 上启动 Gateway：

```sh
export PI_REMOTE_TOKEN='1234567890123456'

./pi-remote-linux-amd64 serve \
  --listen 0.0.0.0:8787 \
  --root /tmp
```

在需要被 Agent 操作的内网 Linux 机器上启动 Worker：

```sh
export PI_REMOTE_TOKEN='1234567890123456'

./pi-remote-linux-amd64 worker \
  --server ws://VPS_PUBLIC_IP:8787 \
  --id office-linux \
  --root /home/user/work
```

`worker` 会主动建立 WebSocket 长连接，断线后自动重连。内网机器不需要
公网 IP、端口映射或入站防火墙规则。

在运行 Pi 的机器上：

```sh
export PI_REMOTE_URL='http://VPS_PUBLIC_IP:8787'
pi
```

或者启动 Pi 后执行：

```text
/remote connect http://VPS_PUBLIC_IP:8787 1234567890123456 --worker office-linux
/remote status
```

此时请求路径为：

```text
Pi -> HTTP -> VPS Gateway -> WebSocket -> office-linux Worker
```

可以连接多台 Worker，只需确保每台机器的 `--id` 唯一：

```text
/remote office-linux
/remote home-linux
/remote win7-test
```

### 5. TLS 可选配置

如果不考虑传输安全，可以像上面的例子一样直接使用 `http://` 和
`ws://`，不需要 Nginx，也不需要证书。

如果需要加密，Go Gateway 可以直接加载证书，同样不需要 Nginx：

```sh
./pi-remote-linux-amd64 serve \
  --listen 0.0.0.0:8787 \
  --tls-cert /path/to/fullchain.pem \
  --tls-key /path/to/privkey.pem \
  --root /tmp
```

开启 TLS 后，Pi 使用 `https://HOST:8787`，Worker 使用
`wss://HOST:8787`。

### 6. Pi 内切换目标

```text
/remote                              # 显示 remote 子命令
/remote status                       # 显示当前目标
/remote list                         # 列出所有已保存机器及在线状态
/remote remove                       # 删除一个已保存连接
/remote refresh                      # 刷新 Worker 列表
/remote off                          # 恢复 Pi 本机工具
/remote office-linux                 # 操作指定 Worker
/remote connect http://HOST:8787 TOKEN --worker ID [备注]  # 更换 Gateway 和目标
```

切换前扩展会等待当前 Agent 回合结束。切换后会把目标 ID、连接方向、
主机名、操作系统、CPU 架构、工作目录和 Shell 类型同步给 Agent。

连接信息会保存到 `~/.pi/agent/remote.json`，包括 Gateway URL、目标、备注和令牌，
方便 Pi 重启后使用 `/remote list` 查看并复用其他连接。Pi 启动时默认使用本机工具，
只有执行 `/remote connect` 或从 `/remote list` 选择连接后才会切入远程；同一个 Pi
进程内执行 `/new` 会继续使用当前远程目标。令牌文件会
以仅当前用户可读写的权限保存；也可以用 `--pi-remote-token` 覆盖保存的令牌。

### 7. 快速检查

查看 Gateway 是否存活：

```sh
curl http://HOST:8787/healthz
```

查看已连接目标：

```sh
curl \
  -H "Authorization: Bearer $PI_REMOTE_TOKEN" \
  http://HOST:8787/v1/targets
```

如果 Pi 看不到 Worker，优先检查：

1. VPS 的 TCP `8787` 是否已放行。
2. Gateway、Worker 和 Pi 的令牌是否完全一致。
3. Worker 日志中是否出现 `worker connected`。
4. `/remote refresh` 后是否出现对应 Worker ID。

## Runtime files

Before building, place the external files under the architecture directory:

```text
internal/runtimebundle/assets/
└── windows_amd64/
    └── bin/busybox.exe
```

The Windows build currently supports amd64 only. Everything in `windows_amd64`,
except `_placeholder`, is embedded in `pi-remote.exe`.

At first shell execution the files are extracted to a content-addressed directory
under `%LOCALAPPDATA%/PiRemote/runtime`. Every file is verified against the
embedded bytes. Agent scripts are never written to a temporary file; they are
sent to BusyBox `sh` over stdin.

For development, skip embedding and point to an existing runtime:

```sh
pi-remote worker \
  --server wss://gateway.example \
  --root C:/work \
  --busybox-path C:/tools/busybox.exe
```

## Build

Use Go 1.20.14 for the Windows 7 binary:

```sh
go mod download
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o bin/pi-remote-windows-amd64.exe ./cmd/pi-remote
```

The embedded runtime and the Windows binary are amd64-only.

## Run

Start the gateway on the machine used by Pi:

```sh
export PI_REMOTE_TOKEN='replace-with-a-long-random-token'
./pi-remote serve --listen 127.0.0.1:8787 --root /path/to/local/workspace
```

Start the remote worker. It makes the outbound connection:

```powershell
$env:PI_REMOTE_TOKEN = 'replace-with-a-long-random-token'
pi-remote.exe worker --server wss://gateway.example --id win7-build-01 --root C:/work
```

When the gateway is not directly reachable from the worker, publish it behind a
TLS reverse proxy. Keep the HTTP RPC listener bound to loopback unless remote API
access is intentionally required.

## RPC

List targets:

```sh
curl -H "Authorization: Bearer $PI_REMOTE_TOKEN" http://127.0.0.1:8787/v1/targets
```

Execute a remote Bash script. Normal JSON is accepted at the HTTP edge; the
gateway converts the entire input object to base64 before sending it over the
worker WebSocket.

```sh
curl http://127.0.0.1:8787/v1/rpc \
  -H "Authorization: Bearer $PI_REMOTE_TOKEN" \
  -H 'Content-Type: application/json' \
  --data-binary @request.json
```

`request.json`:

```json
{
  "target": "win7-build-01",
  "tool": "bash",
  "input": {
    "command": "set -e\nprintf '%s\\n' \"$PATH\"\n",
    "timeout": 30
  }
}
```

The other Pi-compatible inputs are:

```json
{"target":"win7-build-01","tool":"read","input":{"path":"README.md","offset":1,"limit":200}}
{"target":"win7-build-01","tool":"write","input":{"path":"tmp/example.sh","content":"#!/usr/bin/env bash\necho ok\n"}}
{"target":"win7-build-01","tool":"edit","input":{"path":"README.md","edits":[{"oldText":"old","newText":"new"}]}}
```

`target` defaults to the Gateway's local execution target. File tools are confined to `--root` by default,
including symlink checks. `bash` runs with the workspace as its working directory
but, like any real shell, is not an OS sandbox.

## Pi extension and connection directions

The extension in [`pi-extension/index.ts`](pi-extension/index.ts) overrides
Pi's four built-in tools and routes interactive `!` commands. It supports:

- forward mode: run `serve` on the target; a connection without `--worker` uses the Gateway machine
  (the display note is customizable);
- reverse mode: run `serve` beside Pi, run `worker` remotely, and select its
  Worker ID;
- `/remote` target selection without restarting Pi;
- target OS/root/shell synchronization into the model system prompt and session
  context.

See [`pi-extension/README.md`](pi-extension/README.md) for setup examples.

## Windows shell behavior

When the embedded BusyBox runtime is available, the worker starts
`busybox.exe sh -s` directly. Commands such as `ls`, `id`, `whoami`, `grep`, and
`find` are handled by BusyBox without generating applet link files. This uses
BusyBox's `ash` shell syntax rather than Bash syntax. It never silently switches
to CMD or PowerShell.

Timeout and cancellation terminate the Windows Job Object when available, which
also stops child processes. If the process cannot be assigned to a Job Object,
the worker falls back to terminating the Bash process itself.
