# AGENT.md — 设计语言与开发心得

> 本文件供后续 agent/开发者接手时快速对齐。**每次改动都要顺手更新这里**，尤其是踩过的坑和新确立的约定。

---

## 一、产品与架构速览

- 客户端：Wails v2.12 + React 18 + TypeScript + Vite，Go 1.26，模块名 `wailstools`，Windows/amd64。
- 服务端：`server/`，bbolt 存储，Argon2id 认证，wire 协议（QUIC/UDP 优先 → TLS/TCP + yamux 回退），部署在 `s2.admirecn.de:8642`（systemd `wt-server`）。
- 绑定：`internal/app` 的 `App` 方法自动暴露给前端（`window.go.app.App`），前端用自己的 `frontend/src/lib/api.ts` 封装。
- 主要模块：
  - `internal/miproxy`：代理内核（VMess/VLESS/Trojan/SS + 混合 SOCKS5/HTTP 入站），C 版 miproxy 的 Go 重写。
  - `internal/proxysvc`：引擎生命周期、自定义节点、系统代理。
  - `internal/geodata`：geoip.metadb 下载与查询。
  - `internal/netdisk`：网盘（夸克/UC/百度），本地 Cookie 解析，不经服务器。
  - `internal/transfermgr`：可暂停/续传的传输。
  - `internal/hotkey`、`internal/media`（app 内）、`internal/sysproxy`、`internal/updater`。

---

## 二、设计语言（UI）

### 视觉
- 深色为主：背景 `#12141a`，卡片 `--surface`，边框 `--border`，主色 `--accent`（绿），危险 `--danger`，等宽字体用于路径/哈希/地址。
- 卡片布局：`card` + `card-title` + `card-desc`；列表用 `table` 或 `file-row`；状态用 `Tag`（ok/warn/info/bad）。
- 圆角 `--radius`，间距紧凑（8/10/14），信息密度优先。
- **所有图片/视频必须在应用内预览**（MediaViewer），禁止调用系统外部程序打开。

### 品牌图标
- 形状：**squircle**（超椭圆 `|x|^4.2 + |y|^4.2 <= 1`，视觉圆角约 24%），铺满画布。
- 颜色：对角渐变 **mint `#3DD3B0`（左上）→ azure `#3D9BE6`（右下）**；字色近黑 `#08120E`。
- 字形：粗体 **WT**，字框占宽 63% / 高 35.5%，居中；`<= 24px` 时放大到 74% / 44% 并做 gamma 对比度提升，保证托盘里还能认出。
- 字体链：`ariblk.ttf`（Arial Black）→ `arialbd.ttf` → `segoeuib.ttf`，找不到就退回 5×7 点阵字形（`drawBlockMonogram`）。
- 生成：`internal/tray/icon.go` 负责绘制（运行时托盘用）；`go run ./cmd/wt-icon` 写出 `build/appicon.png` 与 `build/windows/icon.ico`（16/20/24/32/40/48/64/128/256 多尺寸，exe/任务栏图标用）。
- `build/` 被 gitignore，**换机器或清空 build 后先跑一次 `go run ./cmd/wt-icon`**，否则 Wails 打包会用旧图标。
- **alpha 语义坑**：Go 的 `image.RGBA` 是**预乘 alpha**；写 PNG 前必须转 `image.NRGBA`（否则半透明边缘出现彩色花纹）；ICO 的 32bpp BMP 需要**还原成直通 alpha**，否则边缘发黑。

### 交互约定（重要）
1. **列表项不放操作按钮**。所有操作走**右键菜单**（`.ctx-backdrop` + `.ctx-menu` + `.ctx-item`）：
   - 订阅卡：右键 → 更新 / 编辑 / 改为走代理(直连) / 删除
   - 节点行：**单击 = 启用**，右键 → 测速
   - 自定义节点行：单击 = 启用，右键 → 测速 / 编辑 / 删除
   - 云端文件：右键 → 下载 / 预览 / 解压 / 重命名 / 删除
   - 本地文件：右键 → 预览 / 上传到云端 / 在资源管理器中显示
   - 网盘文件：右键 → 下载 / 删除
   - 网盘账号卡、截图卡：右键菜单，卡下不放按钮
2. **选择即高亮**：点击订阅卡切换选中（`scope-card selected`），节点表跟随显示该订阅节点。
3. **批量/全局操作**留在页面顶部（新建、同步、全部更新、导入、下载全部）。
4. 术语统一：订阅的「刷新」叫 **更新**；「查看节点」不设按钮，用点击切换。
5. 代理作用域：每个工具一个下拉 — **不走代理 / HTTP 代理 / SOCKS5 代理 / 内部代理**；不要额外的「内部代理」按钮。
6. 设置类改动**无感保存**（防抖自动保存），页面不放「保存」按钮（表单弹窗除外）。
7. 卡片上尽量暴露**事实信息**（路径、大小、时间、错误原文），不要只显示模糊状态。

---

## 三、开发心得 / 踩过的坑（务必先读）

### Wails 绑定
- 绑定命名空间是 `window.go.app.App`；前端 `api.ts` 的 `call()` 会扫命名空间并等待后端就绪。
- Go 的 `nil` 切片会序列化成 `null` → `api.ts` 统一 `coerceList` 归一。
- **结构体返回值可能在前端丢字段**（本项目 geoip 的 `Status` 就出现过：`ready/path` 到前端变 undefined）。
  → 经验：**需要给前端的多字段状态，用 `map[string]any` 返回**（JSON tag 行为确定）；或一次性返回所有字段，避免"两次调用拼状态"。
- 需要确认的调用链，加一个**诊断绑定**（如 `GeoIPDiag`），把真实路径/文件存在性/错误原文直接显示在 UI 上，比猜快得多。

### geoip.metadb（MetaCubeX）
- 记录格式是**字符串** `"cn"` 或**字符串数组** `["us","ca"]`，不是 `{country:{iso_code}}`；按结构体解析会一条都解不出来。
- 用 `maxminddb-golang`：先 `Lookup(&string)`，失败再 `Lookup(&[]string)`；`Networks().Network(&v)` 返回 `(net.IPNet, error)` 两个值。
- 数据库 8~9 MB，可枚举 260 个国家（约 340ms）。
- **不要直接持有 `geoip.metadb` 句柄**：Windows 会报「文件正在使用」，导致替换/删除失败。做法：打开 `geoip.metadb.active` 副本，源文件永不打开；替换前再关闭 reader。
- GitHub 下载顺序：**SNI 绕过（githubx）→ 有代理走代理 → 无代理直连**，每步设超时，避免 UI 卡在「下载中…」。

### 代理内核（miproxy）
- VMess AEAD 的 KDF 是**嵌套 HMAC 链**（C/Xray 一致），写成普通 HMAC 链会被服务端判 `invalid user`。
- 入站本来就是每连接一个 goroutine；并发测试（12 路 / 150ms 后端）应远小于串行时间。
- 启用节点**不再自动改任何工具的作用域**；每个工具单独选择。
- 上次节点持久化在数据目录 `active-node.json`，启动时 `restoreActiveNode()` 恢复。

### 认证 / 服务器
- 服务端 `-local-admin`（默认开）：回环地址且无令牌 = 管理员，用于本机维护；远程仍需账号密码。
- 用户管理用二进制自带命令：`-create-user name:pass`、`-delete-user`、`-set-password`、`-set-role`、`-list-users`。
- **`wtadmin` 已删除，管理员是 `admire`**（密码用户自行修改）。
- 自动登录：启动连接后 `auth.me` 失败要**重试**（更新重启时配置可能未就绪），令牌真失效才清除并提示。

### 更新与发布
- **只从同步服务器按哈希更新**，GitHub 通道已删除；更新链路不走代理。
- 发布脚本 `scripts/build-and-publish.ps1`：自动 **补丁号 +1**（v1.3.x），同步写 README 版本行，**上传前 `upx --best` 压缩**（17.5MB → 6.1MB）。
- 发布需要管理员账号：`$env:WT_USER='admire'; $env:WT_PASS='...'`。

### 部署 / 网络
- 服务器只放行 22 与 7002；8642 的 TCP 被安全组挡住但 **UDP 可用**（客户端 UDP 优先所以正常）。
- 该机器 SSH 偶发握手超时/传输重置：用 `-c aes128-gcm@openssh.com -m hmac-sha2-256`、短连接、大文件走「云盘中转 + 本机 cp」。
- 替换正在运行的二进制会 `Text file busy`：先 `systemctl stop` + `pkill -x wt-server`，或复制到新文件名再启动。
- GitHub 直连经常不可用；推送用 `git -c http.version=HTTP/1.1 push`，必要时经服务器或 API 推送。

### 文件/传输
- 云端/SFTP/FTP 传输统一走 `transfermgr`：暂停=取消上下文并记录 offset，继续=从 offset 重跑；服务端 `files.get/put` 支持 `offset`。
- 云端解压支持 zip/tar/tar.gz，条目路径必须做目录穿越校验。
- 传输记录页分「可暂停的传输」和「实时进度」两块。

### 截图 / 媒体
- Windows 截图用 `CreateDIBSection`（`GetDIBits` 会失败）。
- 全局快捷键用 `RegisterHotKey` + 独立消息窗口；**不能在托盘分支里注册**（托盘关闭时快捷键会失效）。
- 媒体预览走自定义 AssetServer 的 `/media/<token>`，支持 Range（视频可拖动）。

---

## 四、验证与质量门槛

提交前至少跑通：

```powershell
gofmt -l .            # 排除 node_modules / wailsjs
go vet ./...
go test ./...
cd frontend; npx tsc --noEmit
wails build
```

- 关键行为要有测试：miproxy（链接解析/KDF/入站并发）、app（激活不污染作用域、scope 开关）、subscription（链接/Base64/Clash/刷新）、transfermgr。
- 真实节点验证在服务器上做（本机出站高端口被封）。

---

## 五、待办 / 已知问题

- 网盘账号卡、截图卡下方的按钮尚未全部迁到右键菜单（进行中）。
- 订阅页「全部更新」仍在顶部（属批量操作，保留）。
- geoip 国家列表在 UI 只显示前若干项（已把 CN/HK 等常用置顶）。
- 传输暂停对个别不支持 REST/续传的 FTP 服务器可能从头重传。
---

## 六、2026-11-15 决策记录（破坏性改动已授权）

用户明确：当前只有他一个用户，**允许破坏性改动**；此次更新后旧客户端会被删除，不需要兼容旧版协议。

### 6.1 设备上限（每用户最多 5 台）
- 客户端持久化 `server.deviceId`（首次生成 UUID）与 `deviceName`（主机名）。
- 登录/心跳上报设备；服务端 `devices` 桶：`userID -> {deviceId: lastSeen}`。
- 新设备且已达 5 台 → 返回 `device_limit`；30 天未活跃自动过期。
- 任何已登录用户都能在「同步服务」页的「我的设备」卡片自助移除（`admin.devices.self` / `admin.devices.selfRemove`），管理员也可在管理区块按用户移除。

### 6.2 管理页并入「同步服务」
- 仅 `role=admin` 可见；区块：公开注册开关、用户列表、新增/删除用户、改角色、移除设备。
- 服务端新增（全部要求 admin）：`admin.users.list|create|delete|setRole`、`admin.settings.get|set`、`admin.devices.list|remove`。
- **注册的第二个用户起默认 role=user；第一个注册用户自动 admin。**
- 关闭公开注册后，`auth.register` 仅在后端无用户时可用；其余由管理员创建。

### 6.3 登录协议变更（破坏性）
- hello 帧增加 `deviceId` / `deviceName`；`auth.login` 返回 `role`、`deviceCount`。
- `ServerStatus().user` 增加 `role`，前端据此显示管理区块。

### 6.4 教训
- 绑定层禁止按方法名批量做类型转换（v1.3.40 根因）；只对确实返回数组的方法启用 `coerceList`。
- 配置读写必须单一事实来源（内存优先，磁盘仅持久化）；详见 §三。
- **bbolt 杀手坑**：写事务里绝不能再调 `db.View`（或任何自己开事务的方法）。`Update` 持有 meta 锁，嵌套读事务会永久死锁。设备桶改成在同一个 tx 内用 `readDevices(tx, userID)`；`DeleteDevice` 用 `make([]Device,0,...)` 而不是 `devices[:0]`，避免别名写回。
- 新增用户必须显式生成 ID：`store.CreateUser` 不会补 `u.ID`，漏了会以空 key 落库（登录查不到）。
- 设备身份要稳定：`cmd/wt-admin` 把 `server.deviceId` 固定为 `wt-admin-cli`，否则每次 CLI 运行都会占用一台设备名额。
- **CLI 用完即注销**：`wt-admin` / `wt-publish` 登录后 `defer` 调 `admin.devices.selfRemove` 释放自己那台设备，否则 5 台上限会被工具悄悄吃掉（发布几次就登不进去了）。
- 满额后无法登录 = 无法用 RPC 删设备（鸡生蛋）：救援入口只能是服务端离线命令，或已在册的客户端。
- 服务端 CLI（`-list-users` / `-create-user`）在服务运行时打不开 bolt 库（文件锁 5s 超时）；管理操作改用 `cmd/wt-admin` 走 RPC，或先停服务。
- 远程运维细节：`scp` 不认识 `-m`，要用 `-o Ciphers=... -o MACs=...`；**不要用 here-string / 管道给 ssh 传命令**（PowerShell 会补 `\r`，参数变成 `-list-users\r` 报 flag not defined），改成 `ssh host cmd args` 或 `bash -s` 并确保 LF。

### 6.5 管理运维工具
- `cmd/wt-admin`：`-list`（默认）、`-create name:pass[:role]`、`-delete`、`-set-role`、`-devices user`、`-remove-device user:id`、`-registration on|off`、`-settings`、`-device-id`。
- 服务端离线救援（需先 `systemctl stop wt-server`）：`wt-server -data /var/lib/wt-server -list-devices user`、`-remove-device user:deviceID`；`-create-user` 支持 `name:password:role`。
- 服务端部署：`scripts/build-server` 交叉编译 → gzip → scp 到 `/tmp` → 停服务、`gunzip -c` 写新文件、`mv` 覆盖（避免 Text file busy）、启动。
- 本次部署验证：`wt-admin -list` 输出 `admire admin`，`-settings` 返回 `allowRegister=true maxDevices=5`；设备上限实测第 6 台返回 `device_limit`（前 5 台通过，随后已清理探针设备）。
