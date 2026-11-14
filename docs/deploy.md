# 服务端部署

生产实例：`s2.admirecn.de:8642`（Ubuntu 24.04，root）。

## 一键部署

```powershell
# 1) 交叉编译 Linux 服务端
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -trimpath -ldflags "-s -w" -o build/bin/wt-server-linux-amd64 ./server/cmd/wt-server

# 2) 上传并安装
scp build/bin/wt-server-linux-amd64 root@s2.admirecn.de:/usr/local/bin/wt-server
scp deploy/wt-server.service root@s2.admirecn.de:/etc/systemd/system/wt-server.service
ssh root@s2.admirecn.de "chmod +x /usr/local/bin/wt-server && mkdir -p /var/lib/wt-server && systemctl daemon-reload && systemctl enable --now wt-server"
```

## systemd 单元

`/etc/systemd/system/wt-server.service`：

```ini
[Unit]
Description=WailsTools sync server (notes, files, relay, client updates)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/wt-server -data /var/lib/wt-server -tcp 0.0.0.0:8642 -udp 0.0.0.0:8642 -allow-register=true -relay=true -quota-gb 10
Restart=always
RestartSec=3
LimitNOFILE=65535
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=false
ReadWritePaths=/var/lib/wt-server

[Install]
WantedBy=multi-user.target
```

数据目录 `/var/lib/wt-server`：`wails-tools.db`（bbolt）、`files/`（用户文件）、`releases/`（客户端构建）、`tls/`（自签证书）。

## 端口与防火墙

服务端同时监听 **UDP/QUIC 8642（优先）** 与 **TCP 8642（回退）**。

> 该主机所在云厂商安全组默认只放行 22 与 7002。实测：**UDP 8642 可达**（客户端 74 ms 直连成功），**TCP 8642 被安全组拦截**。
> 如需 TCP 回退生效，请在云控制台安全组放行 `8642/tcp` 与 `8642/udp`；本机 `ufw` 处于 inactive，无需额外配置。

若暂时无法改安全组，可用 SSH 隧道让 TCP 回退也可用：

```bash
ssh -N -L 8642:127.0.0.1:8642 root@s2.admirecn.de
# 然后把客户端「同步服务器」地址改成 127.0.0.1:8642
```

## 常用运维

```bash
systemctl status wt-server
journalctl -u wt-server -f
systemctl restart wt-server

# 用户管理（需先停服务，bbolt 为独占锁）
systemctl stop wt-server
/usr/local/bin/wt-server -data /var/lib/wt-server -create-user alice:password123
/usr/local/bin/wt-server -data /var/lib/wt-server -list-users
systemctl start wt-server
```

## 客户端接入

在客户端「同步服务」页填写 `s2.admirecn.de` / `8642`，点击连接：先尝试 UDP/QUIC，失败自动回退 TCP。
注册的第一个账号自动成为管理员，可在「更新」页上传当前客户端构建，供其他客户端自动更新。


## 本地维护访问

服务端以 -local-admin（默认开启）运行时，服务器本机回环地址的连接无需令牌即拥有管理员权限；远程连接仍需账号密码。生产环境如需关闭，在 systemd 单元中加 --local-admin=false。

