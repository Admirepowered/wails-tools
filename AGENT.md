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

---

## 七、2026-11-15 第二批（v1.3.45）

### 7.1 截图：自动上传 + 配额口径
- **根因**：`app.New()` 里 `shots.SetSavedHandler` 被调了两次，第二次把上传回调整个覆盖掉，所以截图只保存、永不上传。现在合并成一个 handler：先 `screenshot:saved`，再按 `screenshots.autoUpload` 上传到 `/screenshots/`。
- 配额口径：上传走普通 `files.put`，服务端 `RecalcUsage` 把 **所有文件元数据**（含 `/screenshots/`）加总，所以截图天然计入同一个 10 GiB 配额，不需要单独字段。上传成功后客户端补发 `server:usage` 事件刷新用量条。

### 7.2 全局快捷键（Ctrl+Alt+S）
- **根因**：窗口在注册线程创建，但 `GetMessageW` 循环跑在另一个 goroutine 的 OS 线程上。`RegisterHotKey` 把 `WM_HOTKEY` 投递到**创建窗口的那个线程**，消息循环不在同一线程就永远收不到 → 快捷键"注册成功但没反应"。
- 修复：`runtime.LockOSThread()` 的专用 goroutine 里完成 建窗 + RegisterHotKey + GetMessage 循环，消息处理与注册在同一线程。`unregister` 从任意线程 `DestroyWindow` 即可（WM_DESTROY → PostQuitMessage 在循环线程执行）。

### 7.3 节点切换改为双击
- 订阅节点表和自定义节点表都是 `onDoubleClick` 才启用（单击只高亮/选中），避免浏览时误触切换代理。文案同步改成「双击节点启用」。

### 7.4 敏感信息同步（密码加密、服务端只存密文）
- **目标**：SSH 主机、SSH 私钥（RSA/Ed25519/ECDSA）、FTP 账号、已授信主机指纹在多设备间同步；服务器只保存密文；密码只存本机。
- **密钥派生**：`Argon2id(password, salt = sha256("wails-tools/vault/v1/" + lower(username)), m=32MiB, t=3, p=2)` → 32 字节。salt 由用户名确定，所以任何设备只要知道密码就能解，不需要额外同步盐。**用原始密码，不用密码哈希**（哈希在服务端，客户端拿不到）。
- **封装**：`internal/vaultsync` 把整个 bundle（hosts+keys+ftp+knownHosts）序列化后用 AES-256-GCM 加密，blob 前缀 `wtv1:`；服务端 `vault` 桶按 `userID\0kind` 存 `{data, updatedAt, size}`，单包上限 4 MiB。
- **同步策略**：拉远端 → `vaultsync.Merge(local, remote)`（按 ID 取 `UpdatedAt`/`CreatedAt` 较新者，单边存在的保留，私钥优先保留有内容的）→ 应用到本地 store（`sshmgr.ApplyHosts/ApplyKeys`、`ftpx.Apply`）→ 有变化再推回。本地编辑后 3 秒防抖自动 push。
- **密码本地存储**：`settings.server.passwordSealed` 用 `secretstore`（机器密钥 `master.key`，AES-GCM）封装；启动/自动登录时解封并重新派生密钥，因此 token 登录也能同步。登录、注册、密码修改都会刷新它。
- **改密流程**：前端先弹确认；服务端 `auth.changePassword` 成功后立刻 `WipeVault(user)` 删除旧密文；客户端 `wipeSecretsAfterPasswordChange()` 清空本机 SSH 私钥、主机密码、FTP 密码，并用新密码重新派生密钥、推送一份"只有列表、没有密钥"的新 bundle。
- **限制**：删除不会跨设备传播（合并是取并集）；如果一定要删干净，需要两端都删，或以后加 tombstone。

### 7.5 教训（第二批）
- 回调"注册两次"是静默覆盖：`SetXxxHandler` 这类 setter 不要连调两次，宁可写一个 handler 干两件事。
- Win32 的消息投递有线程归属：`RegisterHotKey` / `CreateWindow` / `GetMessage` 必须在同一个 OS 线程；Go 侧必须 `LockOSThread`。
- 客户端和服务端都可能"算错配额"：先把数据落到统一的元数据里，配额自然正确，不要为功能单独开计数。
- 加密密钥只能来自**原始密码**：服务端只有哈希，客户端必须自己记密码（本机加密保存），这是设计前提而不是漏洞。

---

## 八、2026-11-15 第三批（v1.3.46）：登录即全量同步

### 8.1 为什么"新设备登录什么都拉不到"
- 旧逻辑只有 `SyncSubscriptions()` 在启动时跑；**笔记从来没有自动同步**（只有页面上点「同步」）；自定义节点、网盘账号根本不在同步范围内；截图只上传不下载。新设备登录=空库。
- 现在 `FullSync()` 一次做完：**笔记 → 订阅+自定义节点 → 加密包（SSH/FTP/网盘）→ 云端截图**，登录、注册、token 自动登录、解锁密钥后都会自动触发（2 秒防抖），侧边栏「同步服务」卡片也有「全部同步」按钮。

### 8.2 冲突规则：时间戳新者胜（全项目统一）
- 笔记 `notes.Merge`：比较 `UpdatedAt`，本地更新则保留并**标记 dirty 排队推送**（旧代码在本地干净时会被更旧的远端覆盖）。
- 订阅 `mergeSubscriptions`：按 `UpdatedAt` 新者胜；自定义节点按 ID 取并集，本地已有的不覆盖。
- SSH 主机/FTP 账号/网盘账号：按 `UpdatedAt` 新者胜；密钥只补缺失，不覆盖本地私钥（本地可能是唯一副本）。
- 笔记编辑/删除后 4 秒防抖自动推送，不用手动点。

### 8.3 订阅文档 v2（含自定义节点）
- 服务端 `subs.put` 存的是原始 JSON，不校验形状，所以直接升级负载：
  `{"version":2,"sources":[...],"nodes":[...]}`；读取时兼容旧的纯数组（当作 sources）。
- 客户端 `parseSyncPayload()` 负责两种形状，`syncDocument` 是 v2 结构。

### 8.4 网盘账号进加密包
- `netdisk.Account` 里有 `UK`、`CookieHash` 等，Cookie 本体在 `cookies.json`（本地 `localvault` 加密）。同步时把 **accounts + 明文 cookie** 放进 `vaultsync.Bundle.Netdisk/NetdiskCookies`，随密码密钥包一起加密上传；`ImportAll` 按 `UpdatedAt` 合并并 `Manager.ReloadFromStore()` 让 UI 立即看到。

### 8.5 截图跨设备
- 截图本来就上传到云端 `/screenshots/`（计入 10GiB）；新设备登录时 `pullCloudScreenshots()` 把服务端有、本地没有的下载回截图目录（单文件 ≤32MB、单次 ≤200 张），画廊直接可见。

### 8.6 教训（第三批）
- "登录成功" ≠ "数据同步"：连接/认证只是通道，必须显式触发一次全量拉取，否则新设备永远是空库。
- 合并策略要写进每个 Merge：只比 `Dirty` 不看时间戳会把更旧的数据写回本地。
- 同步负载要么自带版本号、要么能兼容旧形状——服务端存的是原始 JSON，格式演进由客户端负责。

### 8.7 验证
- `TestNewDeviceRestoresEverything`：设备 A 注册并写入笔记、订阅+节点文档、加密包 → 设备 B 全新安装登录 → 三样都能拉到并解密。
- `TestParseSyncPayload{LegacyArray,Document}`、`TestMergeSubscriptions{NewestWins,RemoteNewer}`、`TestMergeKeepsNewerLocalAndQueuesPush`、`TestImportAllMergesNewerAccountsAndCookies`。

---

## 九、2026-11-15 第四批：设备机器码 + NAT 打洞直连

### 9.1 设备身份：一台机器 = 一个名额
- `internal/machineid`：Windows 读 `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid`，其它平台读 `/etc/machine-id`，兜底 `hostname + 首块网卡 MAC`，再 SHA-256 取前 16 字节。
- `settings.DeviceID()` 首次生成时改成 `m-<machineid>`；已有安装保留旧 id，但登录时**额外上报 `machineId`**。
- 服务端 `registerDevice(userID, deviceID, name, machineID)`：先按 id 命中→只更新 lastSeen；否则删除**同 machineId** 的旧记录，再删"同名且无 machineId"的历史脏数据，最后才做 5 台上限判断。
- CLI（`wt-admin`/`wt-publish`）用 `link.SetMachineID("")` 关闭指纹，避免工具登录把真实设备挤掉。

### 9.2 NAT 打洞（参照 natmap）
- **信令**：服务端新增独立 UDP 端口（默认同步端口 +1，`-p2p-udp` / `-no-p2p` 可配），客户端从**打洞用的同一个 UDP socket** 向它发 `{"s":session,"r":"a|b"}`，服务端据此记录各自的公网 `ip:port` 并互相告知（`server/internal/p2p`）。
- **会话**：`p2p.offer`（发起方）→ 服务端生成 sessionId + 32 字节 secret → 目标设备每 3 秒 `p2p.pending` 拉取 → 两边打洞。
- **打洞**：双方互发带 HMAC 的探针包，收到对方探针即视为直连打通（对称型 NAT 会失败，报 `打洞失败：双方网络不允许直连`）。
- **传输**：打通的 socket 直接交给 quic-go（`quic.Dial` / `quic.Listen` 复用同一个 PacketConn），TLS 用临时自签证书，真正的认证是 QUIC 首流上的 HMAC(secret)——所以没有 CA 依赖也不会被中间人冒充。
- **流复用**：每个操作开一条 QUIC 流，跑现有 `wire` 协议。`p2p.tcp` 请求由对端 `net.Dial` 后双向拷贝；`p2p.ping`/`p2p.capabilities` 用于健康检查，文件管理方法预留在 capabilities 里（`files:false`）。
- **使用前置**：必须先在「同步服务 → 我的设备」右键「连接到此设备」打洞成功；SSH 主机路由选「设备直连（打洞）」、代理链路选「用设备打洞直连」，未打通时直接报错而不是静默回退。
- **端口**：服务器要放行 UDP 同步端口+1（本项目 8643）。客户端「我的设备」卡片有「检测打洞服务」，用一次探针判断端口是否被防火墙拦住。

### 9.3 教训（第四批）
- 设备身份只要有一个随机源（安装 id）就会产生重复行；**机器码 + 服务端按机器码合并**缺一不可。
- 打洞必须在"被服务器观测到的那个 socket"上继续跑数据，否则 NAT 映射换了端口，对端打不通。
- 打洞失败一定要能区分「对端不在线」和「NAT 不允许」，否则用户只会看到"连不上"。
- CLI 工具登录服务器时不能带机器指纹，否则会顶掉真实设备记录。
- QUIC 流上的固定长度握手必须用 `io.ReadFull`，`stream.Read` 可能只返回一部分——这是"认证失败"的真凶。

### 9.4 验证
- `TestDeviceDedupeByMachine`：同 machineId、不同 deviceId 的两次登录只保留一台设备；换机器才新增。
- `TestP2PHolePunchAndRelay`：起一个本地服务端 + 两个客户端（dev-a/dev-b）→ `p2p.offer` / `p2p.pending` → 双端并发打洞 → QUIC 认证 → `p2p.tcp` 经对端中转到本地 echo 服务并原样收到 `hello-p2p`。
- `TestSessionLifecycle` / `TestHandleAnnounceExchange`：会话归属、替换、丢弃与 endpoint 交换。
- 线上验证：UDP 8643 从本机可收发（`{"ok":false,"err":"unknown_or_expired_session"}` 回包），说明云安全组放行了打洞端口。


---

## 十、2026-11-15 第五批（v1.3.49）：本地 SNI 代理

### 10.1 结构
- `internal/sniproxy`：
  - `rules.go`：文本规则表（`domain` / `domain=IP[,IP]` / `.suffix` / `alias` / `!domain` 透传），域名默认包含子域；内置 GitHub / Wikipedia / HuggingFace / Twitter / Pixiv 等默认规则。
  - `resolver.go`：**自迭代 DNS**——先查规则里的固定 IP，再查本地缓存（3 天），再用 DoH（Cloudflare 固定 IP + 正常证书校验），最后系统 DNS；缓存落 `sniproxy/dns-cache.json`。
  - `ca.go`：本地生成 ECDSA P-256 根证书（10 年），按 SNI 签 30 天叶证书；`InstallRoot()` 用 `certutil -user -addstore -f Root` 导入当前用户，失败时返回证书路径让用户手动导入。
  - `proxy.go`：HTTP CONNECT + SOCKS5 双入口；规则内的域名优先 MITM（终止客户端 TLS → 用真实 SNI 重新握手到固定 IP），`!` 前缀或不支持 MITM 时走"原始 TLS 透传"（只换 IP 不解密）；规则外域名走**已有代理**。
- 入口：`ProxyPage` 新增「SNI 代理」卡片（开关、监听地址、出站方式、MITM 开关、导入/复制根证书、规则编辑与恢复默认、连接统计、DNS 缓存数、最近错误）。
- 复用已有代理：出站用 `settings.Proxy.Resolve(ScopeAll)` 构造 `proxyx.Dialer`；`useExistingProxy` 关闭则直连固定 IP。

### 10.2 使用方式
1. 打开开关（默认监听 `127.0.0.1:17891`）。
2. 需要 MITM 时点「导入根证书」（Windows 当前用户信任库；其它平台按提示手动导入）。
3. 把浏览器/系统代理指向该地址；协议是 HTTP 代理（CONNECT）或 SOCKS5，两种都支持。
4. 规则里 `domain=IP` 用固定 IP（绕过 DNS 污染），不写 IP 的交给 DoH 解析。

### 10.3 安全边界
- 只在规则命中的域名上解密；未命中域名永远不 MITM，只做转发。
- 根私钥只存在本机 `sniproxy/root-ca.key`（0600），叶证书内存缓存，不落盘。
- 规则里用 `!domain` 可以强制"只用固定 IP、不解密"，适合证书固定（pinning）的应用。

### 10.4 DoH 走代理（中国网络必须）
- 实测：直连 `cloudflare-dns.com` 的固定 IP（104.16.249.249 等）在本地会被重置，系统 DNS 对 `discord.com` / `github.com` 返回的是污染答案（`face:b00c` 段、Azure 地址）。
- `Resolver.SetUpstream()` 让 DoH 在直连失败后改走已有代理，并且**把目标地址改写成固定 IP 再 CONNECT**（避免代理侧 DNS 也被污染）；默认端点是 Cloudflare（`/dns-query`）+ Google（`/resolve`/8.8.8.8）。
- 直连只尝试前 2 个端点、每个请求 6s，避免把总超时耗在直连上。

### 10.5 `discord.com=162.159.128.233` 诊断结论（2026-11-15）
- 同一 IP + SNI `example.com` → TLS 成功（证书 CN=example.com）；同一 IP + SNI `discord.com` 直连 → **握手阶段被 RST/EOF**。
- 从境内服务器（s2）经 relay 发同样的 ClientHello 也被重置；把 ClientHello 从香港节点发出（`CONNECT 162.159.128.233:443`）→ **TLS 成功，证书 CN=discord.com**，`GET /` 200，`/api/v9/gateway` 返回 `wss://gateway.discord.gg`。
- 结论：**不是 Cloudflare 拒绝**，而是链路上按 SNI 重置（固定 IP 只能解决 DNS 污染，不能解决 SNI 阻断）。
- **关键修正（用户指正）**：MITM 的目的不是"带真 SNI"，而是**把出站 ClientHello 的 SNI 抹掉**。实测：
  - 直连 `162.159.128.233` + 无 SNI → TLS 成功（CF 默认证书就是 discord.com）；
  - 直连同 IP + `SNI=discord.com` → 被 RST。
  - 因此 SNI 代理已改成**所有出站握手一律不发送 SNI**，站点由 HTTP `Host` 头（HTTP/2 的 `:authority`）决定；服务器不接受无 SNI 握手就按失败处理，不重试带 SNI。
  - 实测（不挂任何上游代理）：`discord.com/` 200、`/api/v9/gateway` 200、`github.com` 200、`api.github.com/zen` 200。
- 已把 Discord 主站（`discord.com` / `.discord.com`）加入默认规则。

### 10.5.1 CDN 域名必须带 SNI（2026-11-15 补充）
- 反例：`https://stable.dl2.discordapp.net/distro/app/stable/win/x64/.../DiscordSetup.exe`
  - 本机直连 CDN 真实 IP（104.16.251.55）→ TCP 超时；直连 Discord 通用 IP + **无 SNI** → RST。
  - 经代理（带 SNI）→ **206 Partial Content**，说明 Cloudflare 并没有拦下载。
  - 走 SNI 引擎（MITM + 无 SNI + 162.159.128.233）→ **403**（CF 回落到该 IP 的默认 zone：discord.com / 甚至 *.usgs.gov）。
- 结论：Cloudflare 的 CDN 子域（`dl2.*`、`cdn.*`）按 **SNI 选 zone**，不发 SNI 就一定拿不到；这类域名应**留在规则外**，走代理链路（客户端的 SNI 在加密隧道里，本地网络看不到）。
- 默认规则已移除 `discordapp.net` / `discordapp.com`，只保留 `discord.com`。
- DoH 顺序调整：配置了代理链路时**优先穿代理**（目标改写成固定 IP 再 CONNECT），避免直连被墙时把解析总超时耗光。

### 10.5.2 “带 SNI 直连”能不能救 CDN 域名（2026-11-15 实测，结论：不能）
对本机多个候选 IP（系统 DNS 的 128.242.240.221 / 104.16.251.55，以及 CF 边缘 104.16.248.55 / 104.16.250.55 / 162.159.128.233 / 162.159.129.233 / 104.16.249.249）做了三组对比：

| 组 | SNI | 结果 |
|---|---|---|
| 1 | `stable.dl2.discordapp.net`（真实） | 全部 TCP 超时或 **握手被 RST** |
| 2 | 不存在的名字 | 部分 IP 能握手（默认证书）→ 403 |
| 3 | `example.com` | 同一批 IP **握手成功**（证书 example.com）→ 403 |

第 3 组证明 IP 与 443 都是通的；第 1 组只有真实 SNI 被重置 → **是本机链路的 SNI 阻断，不是 Cloudflare 拒绝**。
因此“加一个 `@domain` 让 SNI 引擎发真 SNI 做本地直连”没有意义：带 SNI 直连一样连不上。CDN 域名只有两条路：
1. 留在规则外，走代理链路（客户端 SNI 在加密隧道内，实测 206）；
2. `!domain` 原始透传（也必须挂在代理链路上，否则 SNI 照样裸奔被阻断）。

### 10.6 验证
- `TestParseAndMatchRules` / `TestDefaultRulesCoverGithub`：规则解析、别名、子域、`!` 透传。
- `TestMitmTunnelThroughProxy`：本地起 TLS 源站（8443）→ SNI 代理 MITM（规则 `origin.test=127.0.0.1`）→ 客户端用根证书校验通过并回显 `ping-mitm`，且 `stats.mitm==1`。



---

## 十一、2026-11-15 第六批（v1.3.52）：SNI 分流并入本地代理

### 11.1 只有一个监听端口
- `miproxy.ServerConfig` 新增 `Intercept(miproxy.InterceptInfo) bool`：在 SOCKS5 / HTTP CONNECT 解析出目标后、链路拨号**之前**调用。
  - `InterceptInfo{Client, Reader, Host, Port, Kind}`；`Reader` 是 inbound 已经缓冲的字节（TLS ClientHello 可能在其中）。
  - 返回 `true` = 拦截者接管连接（并且**由它自己写**成功应答：HTTP `200 Connection established` / SOCKS5 回复）。
- `proxysvc.StartOptions.Intercept` 透传；`app.startEngine` 把它设成 `a.sniIntercept`。
- `sniproxy.Handler`（无监听器版本）：`NewHandler(opts)` + `HandleTunnel(conn, host, port)` + `Match(host)` + `Stats()`；独立 `Start()` 仅留给测试用。
- 判定顺序：**SNI 分流 → 代理链路**。只要 `SNIProxy.Enabled=false` 或域名不在规则里或端口不是 443/8443，就 `return false`，走原来的节点链路。

### 11.2 出站顺序
- SNI 接管后：客户端 TLS 由本地根证书终止 → 出站重新握手到固定/DoH 解析出的 IP，**不发送 SNI**，站点由 `Host` 头决定。
- `useExistingProxy` 打开时，SNI 出站也走同一套链路（`buildChain()` 的节点/服务器中转/打洞设备），所以"本地网络连不上目标 IP"时仍可由节点代连；关闭则直连。
- DoH 解析同样在直连失败后走这条链路，并把目标改写成固定 IP 再 CONNECT（避免节点侧 DNS 污染）。

### 11.3 状态与开关
- SNI 卡片不再有监听地址；状态显示「与本地代理共用端口」+ 接管/MITM/透传/失败计数 + DNS 缓存数 + CA 路径。
- 开关只控制 SNI 引擎，不影响代理链路本身；关闭后所有流量照旧走节点。

### 11.4 验证
- `TestLocalProxyInterceptsRulesOnly`：本地 mixed 代理 + 拦截器 → 规则内域名被 SNI 接管（源站确认收到的握手 **ServerName 为空**），非规则域名落到链路。
- 实机（真实网络、直连出口）：经本地代理 `https://discord.com/api/v9/gateway` 200、`https://github.com/` 200，`example.com`（不在规则内）走链路。

---

## 十二、2026-11-15 CI 发布流程（.github/workflows/build-release.yml）

- **只手动触发**：`workflow_dispatch`，输入项 `build` / `release` / `version` / `prerelease` / `upx`。
  - `build=true` 构建全部产物；`release=true` 才会创建 GitHub Release（需要 build 同时为 true）。
  - `version` 留空则读 `internal/version/version.go`；填了就在构建前改写该常量，保证产物与 tag 一致。
- 产物：
  - 服务端：`wt-server-linux-amd64.gz`（linux amd64，CGO 关闭、`-trimpath -s -w`）、`wt-server-windows-amd64.exe`。
  - 客户端：Ubuntu 24.04 amd64（`wails build -tags webkit2_41` + tar.gz）、Windows amd64（可选 UPX）、macOS arm64（`.app` → zip，ad-hoc 签名）。
- 关键点：
  - `build/` 被 gitignore，所以每个客户端任务在 `wails build` 之前先跑 `go run ./cmd/wt-icon` 生成 appicon/windows icon。
  - Linux 24.04 必须 `libwebkit2gtk-4.1-dev` + `-tags webkit2_41`。
  - Wails CLI 固定 `v2.12.0`，Go 版本由 `go.mod`（`go-version-file`）决定。
  - Windows 客户端默认 `-webview2 download`，UPX 由 `upx` 输入控制。
- 手动排查：GitHub Release 用 `softprops/action-gh-release@v2`，`generate_release_notes` 打开、`fail_on_unmatched_files` 打开，避免少资产却发成功。
- **macOS 产物路径**：Wails 的 `.app` 目录名来自 `wails.json` 的 `name`（`wails-tools.app`），而里面的可执行文件才是 `outputfilename`（`WailsTools`）。workflow 里用 `find build/bin -maxdepth 1 -name '*.app'` 定位，再复制成 `dist/WailsTools.app` 压缩，压完删掉目录（否则 `files: dist/*` 会把目录当资产上传而失败）。
- **macOS runner 兼容**：`runs-on: macos-14` 在部分账号会解析成 `macos-14-arm64-us-default` 并报 "configuration is not supported"（私有仓库/区域无 arm64 容量）。所以加了 `macos_runner` 输入（默认 `macos-latest`，可改 `macos-15`/`macos-14`/`macos-13`）和 `build_macos` 开关；release 任务用 `always() + !contains(needs.*.result,'failure')`，macOS 被跳过时其余产物照样发。





---

## 十三、2026-11-15 Discord Gateway WebSocket 诊断

`wss://gateway.discord.gg/?encoding=json&v=9&compress=zlib-stream` 实测：

| 路径 | 结果 |
|---|---|
| 本地直连 + 真 SNI | TLS 握手被 RST（SNI 阻断） |
| 本地直连 + 无 SNI（162.159.128.233） | 握手 OK（证书 discord.com）→ **403**（zone 不匹配） |
| 经代理 :48952 | TLS OK（证书 discord.gg）→ **101 Switching Protocols** + `sec-websocket-accept` + 首帧 opcode=0x2、`x\xda…`（zlib 流的 hello 帧）✅ |
| 经代理 :7890 | TLS OK（证书 gateway.discord.gg）→ **403**（该出口 IP 被 Cloudflare/Discord 风控） |

- 结论：gateway 能代理，**只要出口干净**；403 是出口 IP 被拒，不是代理协议问题。
- 判定"真成功"：`101` + `Sec-WebSocket-Accept` + 二进制帧（`0x2`，zlib 压缩的 `{"op":10}`）。收到 403 HTML 就说明被拒。
- `gateway.discord.gg` 属于 CDN/网关域名，**不要**塞进 SNI 规则（无 SNI 必 403），让它走代理链路。

---

## 十四、2026-11-15 Discord 本地直连打通（v1.3.54）

### 14.1 关键规律：每个 zone 有"专属 IP"
Cloudflare 上 Discord 各 zone 的 A 记录 IP，**默认证书就是那个 zone**，因此"固定 IP + 不发 SNI + Host 头"可以完全本地直连：

| 域名 | 专属 IP（实测） | 无 SNI 默认证书 | 结果 |
|---|---|---|---|
| discord.com | 162.159.128.233 | discord.com | GET / → 200 |
| gateway.discord.gg | 162.159.130.234 / .133 / .134 / .135.234 | discord.gg | WSS → 101 + zlib 首帧 |
| cdn.discordapp.com | 162.159.134.233 / .135 / .130.233 | discordapp.com | 资源 200 / 404（zone 正确） |
| stable.dl2.discordapp.net | 34.126.226.51（GCP，不是 CF） | dl2.discordapp.net | 安装包 → **206 Partial Content** |

- 用户给的 104.17.112.188 / 104.21.2.112 / 172.67.129.30 属于 CF 的"通用 anycast IP"，**没有默认 zone**：不发 SNI → `tls: handshake failure`；发 SNI → 被本地链路 RST → 这几个 IP 用不了。
- 所以固定 IP 必须**逐域名使用它自己的 A 记录 IP**，不能拿一个 Discord IP 通吃所有 Discord 域名。

### 14.2 本地 DoH 也必须不发 SNI
- 之前 DoH 直连 `cloudflare-dns.com` 一直被 RST；现在改成：**连接固定 DoH IP、握手不发 SNI**，再用 `x509.VerifyOptions{DNSName: host}` 手动校验证书（CF 对 104.16.249.249 的默认证书就是 cloudflare-dns.com）。
- 效果：不挂任何代理，`github.com` 也能 `via=doh` 解析成功。

### 14.3 实测（无上游、纯本地出口）
```
本地 DoH: github.com -> 172.182.252.133 via=doh
https://discord.com/                         200
https://cdn.discordapp.com/attachments/...   404（fake 路径，zone 正确）
DiscordSetup.exe (Range 1K)                  206 application/x-msdownload  MZ...
wss://gateway.discord.gg/...                 101 Switching Protocols + x\xda 首帧
```

---

## 十五、2026-11-15 规则下发 + 内置直连节点（v1.3.55）

### 15.1 SNI 默认规则优先从服务器拉取
- 服务端新增 `sni.rules.get`（登录可读）/ `sni.rules.put`（仅 admin，存 `meta` 桶的 `sniRules` + `updatedAt/by`，上限 1 MiB）。
- 客户端：
  - 「恢复默认」按钮**仍是唯一入口**，但改为：先 `sni.rules.get`，拿到就用（toast 提示来自服务器），拿不到/为空才回退编译进二进制的 `sniproxy.DefaultRules`。
  - `SNIProxy.Rules` 为空（新装/未自定义）时，开启 SNI 会在后台拉一次服务器规则并热重建引擎，不打断使用。
  - 用户自定义过的规则（非空）**永远不会被服务器覆盖**，只影响"恢复默认"。
- 运维：`cmd/wt-admin` 新增 `-sni-rules`（查看）/ `-sni-rules-upload file`（发布文件）/ `-sni-rules-defaults`（发布客户端内置默认）。本次已把 92 行、2721 字节的默认规则发布到服务器（`2026-11-15T15:44:23Z`）。

### 15.2 内置直连节点（不可删除）
- `proxysvc.BuiltinDirectNode()`：`ID=builtin-direct`，名称「直连（本机出口）」，`Protocol=direct`。
  - `OpenNodes` / `OpenNodesFresh` 都会确保它存在（`ensureBuiltin`，插在最前）。
  - `Delete` / `Save` 对它直接返回错误（内置直连节点不能删除/修改）。
- 选中它 = 激活本地引擎但上游直连：客户端流量进入本地 mixed inbound，再由引擎自己拨号（若链路模式是 server/nodes/device，则按链路绕一圈）。
- UI：自定义节点列表里带「内置」标签，右键只有「测速」；地址列显示「本机出口」；代理链路的中转节点列表里已把它过滤掉（拿它当中转跳没有意义）。
- 验证：`TestBuiltinDirectNodeIsPermanent`（存在性、不可删、不可改、重开不重复、损坏文件重建后仍在）与 app 层节点测试已更新。

---

## 十六、2026-11-16 VLESS REALITY + Vision（v1.3.56）

### 16.1 之前为什么连不上
订阅里的「🇺🇸美国凤凰城1号」是 `vless://…?security=reality&flow=xtls-rprx-vision&fp=ios&sni=tv.apple.com&pbk=…&sid=78f5a5a5`。
旧实现只把 `security=reality` 当成普通 TLS（`tls.Client` + SNI），既没有 REALITY 认证，也没有 Vision 帧，服务端直接断开。

### 16.2 实现（参照 E:\Project\Pulse\mihomo）
- 依赖 `github.com/metacubex/utls v1.8.4`（与 mihomo 同款 uTLS 分支，`UConn.HandshakeState` 暴露了 REALITY 需要的 key share 私钥）。
- `internal/miproxy/reality.go`：移植 `component/tls/reality.go` 的客户端逻辑
  - 按 `fp=` 选择 ClientHello（ios/chrome/firefox/safari/edge/random 等）；
  - 去掉 `X25519MLKEM768` key share（老 REALITY 服务端不认）；
  - 用 `ECDHE(pbk)` + HKDF("REALITY") + AES-GCM 生成 session id 里的认证串；
  - `VerifyConnection` 里用 ed25519 公钥做 HMAC-SHA512 校验服务端签名，校验不过再回退正常证书校验。
- `internal/miproxy/vision.go`：`xtls-rprx-vision` 帧层
  - 写：`[uuid(仅首帧)][cmd=0][contentLen][padLen][payload][padding]`，首帧长 padding（≥900）隐藏头部；
  - 读：解帧，遇到 `end/direct` 命令后切回裸流。
- VLESS 请求/响应头是**裸的**（这点和 mihomo 一致：Vision 只包 payload），请求头里的 addons 用 protobuf 编码 `flow`（`0a <len> flow`）。
- `Node` 新增 `reality`、`realityPublicKey`、`realityShortId`、`fingerprint`；分享链接解析 `pbk/sid/fp`，Clash 配置解析 `reality-opts.public-key/short-id` 与 `client-fingerprint`。
- 订阅页节点表加了 `reality` / `vision` 标签。

### 16.3 实测（真实节点 142.202.242.179:443）
```
握手 492ms；HTTP(80) → 204 No Content
内层 TLS 1.3 (0x304) 证书 *.gstatic.com；HTTPS → 204 No Content
```
单测：链接解析、reality 参数校验、Vision 写帧布局、Vision 读帧（含 direct 切换后裸流）、addons 编码。

### 16.4 v1.3.57：Vision 的 splice 语义（浏览器 HTTP/2 才真正可用）
v1.3.56 只用 curl（只支持 HTTP/1.1）验证过，浏览器一上 HTTP/2 就 `ERR_CONNECTION_CLOSED` / `bad record MAC`，因为 Vision 不是"纯 padding"协议：

- 客户端**一直 padding** 时，Xray 服务端只要在未 padding 的字节流里看到 `17 03 03`（TLS 应用数据）就会切到 **splice**：它把内层 TLS 记录**直接写到裸 TCP**（绕过外层 TLS），先发一个 `cmd=0x02 (direct)` 的块作为标记。
- 所以客户端也必须跟着切：读到 `cmd=2` 之后**停止解 padding，直接读裸 TCP**；写方向在首个 `17 03 03` 写时发 `cmd=2` 块，之后**直接写裸 TCP**。
- 关键坑：切换瞬间 TLS 栈里可能已经缓冲了明文（`input`/`rawInput`），必须先搬出来（Xray 用反射访问这两个字段，我们也用 `unsafe` + `reflect` 照做），否则丢字节 → `bad record MAC`。
- 另一个坑：`cmd=0x01 (end)` 只表示"padding 结束"，**仍然走外层 TLS**，不能当成 splice；之前把 1 也当 splice，导致 Chrome 的 `edge.microsoft.com`、`www.google.com.hk` 等连接 `ERR_SSL_PROTOCOL_ERROR`。
- 实现：`visionConn{tls, raw, inner}`；写：padding 块→外层 TLS，splice 后→raw；读：解 padding→`cmd=1` 转"裸 TLS 直读"→`cmd=2` 转 raw 并把 TLS 缓冲并入。
- 验证：Go 的 h2 客户端与真实 headless Edge 都通过美国 REALITY 节点打开 example.com / google.com.hk / youtube.com / github.com（标题正确）。

