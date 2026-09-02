# pi-remote

`pi-remote` 通过经过认证的 RPC 接口暴露 Pi 的 `read`、`bash`、`edit`、`write` 和远程文件搜索能力。远程 Worker 主动建立出站 WebSocket 连接，因此目标机器不需要开放入站端口。

项目源码以兼容Windows 7/2008为目标。

## 功能

`pi-remote` 支持两种连接方式：

- 正向连接：在目标机器上运行 `serve`，Pi 直接连接目标机器。
- 反向连接：在 Pi 旁边运行 `serve`，目标机器以 `worker` 身份主动连接；
  这种模式下 Pi 和目标机器都不需要开放入站端口。

Pi 扩展会接管 `read`、`bash`、`edit`、`write`、文件补全和交互式 `!` Bash 命令。

## 安装 Pi 扩展

将扩展复制到 Pi 的自动加载目录：

```sh
mkdir -p ~/.pi/agent/extensions/pi-remote
cp pi-extension/index.ts ~/.pi/agent/extensions/pi-remote/index.ts
cp pi-extension/README.md ~/.pi/agent/extensions/pi-remote/README.md
```

重启 Pi 后扩展会自动加载。Gateway 和 Worker 必须使用相同的 Token，Token 长度至少为 8 个字符。通过 `/remote connect` 在 Pi 中输入 Token，首次连接成功后会自动保存。

## 正向连接

在目标 Linux 机器上启动 Gateway：

```sh
./pi-remote-linux-amd64 serve \
  --token 'replace-with-a-long-random-token'
```

Pi 内连接：

```text
/remote connect http://TARGET_IP:8787 replace-with-a-long-random-token 我的开发机
/remote status
```

连接命令不指定 `--worker` 时，默认操作 Gateway 所在机器。末尾可选文本是显示备注。反向连接的 Worker 可这样选择：

```text
/remote connect http://VPS:8787 replace-with-a-long-random-token --worker office-linux 我的办公机
```

## 使用公网 VPS 中转（无 TLS）

当 Pi 和目标机器都处于内网时，可以使用以下拓扑：

```text
Pi  --HTTP-->  公网 VPS Gateway  <--WebSocket--  Worker
```

即使使用 `http://` 和 `ws://`，RPC 内容与 Worker 消息也会使用 AES-256-GCM 加密，并使用带时间戳和随机 nonce 的 HMAC 认证。被动抓包者无法读取 Token、命令、文件内容或执行结果。由于 VPS 持有共享 Token，VPS Gateway 本身仍然可以解密流量。

在 VPS 的云安全组和系统防火墙中放行 TCP `8787`。

在公网 VPS 上启动 Gateway：

```sh
./pi-remote-linux-amd64 serve \
  --token 'replace-with-a-long-random-token' \
```

在内网目标机器上启动 Worker：

```sh
./pi-remote-linux-amd64 worker \
  --token 'replace-with-a-long-random-token' \
  --server ws://VPS_PUBLIC_IP:8787 \
  --id office-linux
```

Worker 会主动建立 WebSocket 长连接，并在断线后自动重连。目标机器不需要公网 IP、端口映射或入站防火墙规则。

在 Pi 机器上：

```text
/remote connect http://VPS_PUBLIC_IP:8787 replace-with-a-long-random-token --worker office-linux
/remote status
```

请求路径为：

```text
Pi -> HTTP -> VPS Gateway -> WebSocket -> office-linux Worker
```

可以连接多台 Worker，但每台机器的 `--id` 必须唯一：

```text
/remote office-linux
/remote home-linux
/remote win7-test
```

## 可选的 TLS 配置

应用层加密已经覆盖无 TLS 场景。如果需要额外的传输层保护，Go Gateway 可以直接加载证书：

```sh
./pi-remote-linux-amd64 serve \
  --token 'replace-with-a-long-random-token' \
  --listen 0.0.0.0:8787 \
  --tls-cert /path/to/fullchain.pem \
  --tls-key /path/to/privkey.pem \
  --root /tmp
```

启用 TLS 后，Pi 使用 `https://HOST:8787`，Worker 使用 `wss://HOST:8787`。

## 在 Pi 中切换目标

```text
/remote                              # 显示 remote 子命令
/remote status                       # 显示当前目标
/remote list                         # 列出已保存连接及在线状态
/remote remove                       # 删除一个已保存连接
/remote refresh                      # 刷新 Worker 信息
/remote off                          # 使用 Pi 本机工具
/remote office-linux                 # 选择一个 Worker
/remote connect http://HOST:8787 TOKEN --worker ID [备注]
```

切换会等待当前 Agent 回合空闲。选定目标的元数据会保存到 Pi 会话，并注入后续模型上下文，包括目标 ID、连接方向、在线状态、主机名、操作系统、CPU 架构、工作目录和 Shell 类型。

连接信息会保存到 `~/.pi/agent/remote.json`，包括 Gateway URL、目标、备注和共享 Token。文件权限为 `0600`；也可以用 `--pi-remote-token` 覆盖保存的 Token。

## 存活检查

唯一有意保持明文的端点是 `/healthz`：

```sh
curl http://HOST:8787/healthz
```

`/v1/targets` 和 `/v1/rpc` 使用 Token + HMAC + AES-256-GCM 协议，不接受明文 `Authorization: Bearer` 请求头。请使用 Pi 扩展，或实现相同的安全协议。

## 运行时文件

构建 Windows 程序前，将运行时文件放在：

```text
internal/runtimebundle/assets/
└── windows_amd64/
    └── bin/busybox.exe
```

Windows 构建目前只支持 amd64。`windows_amd64` 下除了 `_placeholder` 以外的文件都会嵌入 `pi-remote.exe`。

第一次在 Windows 上执行 Shell 命令时，嵌入的文件会释放到 `%LOCALAPPDATA%/PiRemote/runtime` 下按内容寻址的目录。每个文件都会和嵌入内容进行校验。Agent 脚本通过 stdin 发送给 BusyBox `sh`，不会写入临时脚本文件。

开发时可以跳过嵌入，指定已有运行时：

```sh
pi-remote worker \
  --token 'replace-with-a-long-random-token' \
  --server ws://gateway.example \
  --root C:/work \
  --busybox-path C:/tools/busybox.exe
```

## 构建

Windows 7 程序使用 Go 1.20.14：

```sh
go mod download
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o bin/pi-remote-windows-amd64.exe ./cmd/pi-remote
```

嵌入的运行时和 Windows 程序均只支持 amd64。

## Windows Shell 行为

嵌入的 BusyBox 运行时可用时，Worker 会直接启动 `busybox.exe sh -s`。`ls`、`id`、`whoami`、`grep` 和 `find` 等命令由 BusyBox 处理，不会生成 applet 链接文件。使用的是 BusyBox 的 `ash` 语法，而不是 Bash 语法，也不会静默切换到 CMD 或 PowerShell。

如果可用，超时和取消会终止 Windows Job Object，同时停止子进程。如果进程无法加入 Job Object，Worker 会退回到直接终止 Bash 进程。
