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


---

## 十七、2026-11-16 BT 下载与离线下载（v1.3.58 / v1.3.59）

### 17.1 下载增强（v1.3.58）
- `DownloadSettings.Cookie`：全局 Cookie 请求头；下载页新增单任务 Cookie（留空用全局值）。`dlengine` 的 `buildRequest` 在没有显式 Cookie 时注入，探测/分段/续传都生效。
- 线程/并发：沿用 `ConnectionsPer`（单任务线程，1–32）与 `MaxConcurrency`（同时下载数），下载页可直接改连接数。
- SNI 分流**固定走代理链路**：`applySNIProxy` 不再看 `UseExistingProxy`，统一用 `sniChainDialer()`（= `buildChain()`）：`off` 直连、`server` 经同步服务器中转、`nodes` 经 1–2 个节点、`device` 经打洞设备。DoH 也走同一链路。

### 17.2 BT 客户端（v1.3.59）
- 依赖 `github.com/anacrolix/torrent v1.62.0`；**Windows/CI 用 `CGO_ENABLED=0` 构建**（torrent 的 uTP/sqlite 组件是 cgo-only，禁用 cgo 时自动排除；Wails Windows 本身不需要 cgo，已实测）。
- `internal/torrentdl`：`Download(ctx, magnet, Options{Dir, Progress, OnMetadata})`，内存 piece completion（`storage.NewMMapWithCompletion` + `NewMapPieceCompletion`），不再往下载目录写 `.torrent.bolt.db`。
- `dlengine`：`Task.Kind = http|magnet`；`Add` 识别 `magnet:`，文件名取 `dn=`；`run` 分支到 `runMagnet`（进度节流、暂停=ctx 取消、续传靠文件已存在+重哈希）。
- 下载页：磁力链可直接粘贴（探测按钮对 magnet 禁用），任务卡显示进度与状态。
- 实测（官方 Sintel 种子）：4 秒拿到 metadata（129,302,391 字节 / 11 文件），速度约 200 KB/s 正常下载。

### 17.3 服务端离线下载
- 存储：`store.OfflineTask`（meta 桶 `offline:<id>`）+ `Get/Put/List/DeleteOfflineTask`。
- API：`offline.add`（magnet）/ `offline.list`（含 `usedBytes`/`quotaBytes`）/ `offline.delete`（`purge` 连文件一起删）。
- 下载落盘到用户文件根下的 `离线下载/<torrent 名>/`，完成后递归 `PutFileMeta` 注册（跳过点文件），再 `RecalcUsage` —— **与截图共用一个 10 GiB 配额**，并在「文件」页可直接取回。
- 配额在拿到 metadata 后立即校验：`used + total > quota` 直接失败并带 `quota_exceeded` 信息。
- 运维：`wt-admin -offline-add <magnet>` / `-offline-list`。
- 实测：129 MB 的 Sintel 完整下完，任务 `completed`，文件出现在 `/var/lib/wt-server/files/u-…/离线下载/Sintel/`，`usedBytes` 从 59 MB 增长到 188 MB（增量正是下载大小）。

### 17.4 v1.3.60：空列表崩溃 + 删除语义
- Go 的 nil slice 序列化成 JSON `null`，前端 `offline.tasks.length` 直接抛 `Cannot read properties of null (reading 'length')`，整个页面白屏。
  - 服务端 `offlineStatus` 现在始终返回 `[]`（`if tasks == nil { tasks = []store.OfflineTask{} }`）；
  - 前端统一 `const offlineTasks = offline?.tasks ?? []`，并对返回做归一化。
- 删除语义调整：默认「删除任务」只删记录、**保留文件**（文件页可自行清理）；完成任务多一个「连文件删除」，显式 `purge=true` 才会 `os.RemoveAll`。


---

## 十八、2026-11-16 打洞重做（参考 n2n）+ 设备号（阶段 1/…）

### 18.1 n2n 的做法与我们的问题
n2n 的"打洞"本质是：两端都用**同一个 UDP socket** 持续向 supernode 注册，supernode 记住各自公网地址；两端拿到对方地址后**同时**向对方发包，把各自 NAT 上的映射打开，直连成功后 supernode 只做兜底。
我们旧实现的问题：
- 每次打洞都 `ListenUDP(..., nil)` 随机端口，NAT 映射随机、难复现；
- 只朝"supernode 看到的那个地址"发，**没有端口预测**，对称型 NAT 必失败；
- 收到对端包后仍用**宣告地址**做 QUIC 拨号，实际回包地址若不同就断。

### 18.2 阶段 1 已实现
- `p2p.Options.LocalPort`：优先绑定固定端口（`ProxyRuntime.P2PPort`，0=随机），映射稳定可预测。
- `punchProbe` 返回**实际回包地址**，QUIC 用它拨号；超时 2 秒后同时对 `±1..±10` 预测端口发包（对称 NAT 的连续端口分配）。
- 双方仍然"拿到对方地址就开始互发"，保持同时打洞。
- **9 位设备号**：`Device.Number`（`000000000` 风格且首位非零），`AddDevice`/`TouchDevice` 时分配，全局唯一（`allocDeviceNumber` 扫 `bucketDevices`），`FindDeviceByNumber` 供后续"输入对方设备号发起穿透"用；管理工具与客户端「我的设备」都会显示，右键可复制。
- 单测：`server/internal/store/devnum_test.go`（分配长度 9、按号查得到、Touch 不变号）。

### 18.3 阶段 2：按设备号邀请 + 接受 + 端口映射（v1.3.62）
- 服务端 rendezvous 新增 `Request` 模型（`CreateRequest`/`Requests`/`Respond`/`RequestState`/`CancelRequest`）：
  - 邀请带发起方设备号/名称，TTL 90s；接受时才 `CreateSession` 并把 `session`/`secret` 写回邀请，**未接受不交换任何地址**。
  - 角色锁：`roleCountsLocked` 统计 2 分钟内 `To==device`（被穿透）/`From==device`（穿透）的会话数；
    被穿透端不能发起（`CreateRequest` 返回"当前设备作为被穿透端正在被使用"），正在穿透的设备不能接受（`Respond` 返回"当前设备正在穿透其他设备"）。
    被穿透端可同时接受多台（只限制"不能反向发起"）。
- API：`p2p.request`（按 9 位设备号查 `FindDeviceByNumber`，跨账号允许）、`p2p.requests`、`p2p.respond`、`p2p.requestState`、`p2p.cancel`。
- 客户端：
  - `P2PConnectByNumber`：邀请 → 轮询状态（最长 95s）→ 接受后用 `punch` 建链并 `Serve`；旧的"右键连接到此设备"会先解析设备号再走同一条路径。
  - 后台 watcher 改为轮询 `p2p.requests`，**不再自动应答**，只落邀请列表并 `emit("p2p:invite")`；UI「我的设备」卡片里显示"XX 想穿透本机 #设备号"，有接受/拒绝按钮。
  - 端口映射：`P2PPMapPort(device, local, remote)` 在 `127.0.0.1:local` 监听，每条本地 TCP 连接开一条 QUIC 流让对端拨 `127.0.0.1:remote`，连接结束即关流（单连接单流、用完即释放）；`P2PUnmap` 删映射；`p2pHub.drop` 关闭打洞时**连带关闭该设备的全部监听**。
- 测试：`TestP2PInviteAcceptAndMap`（9 位设备号邀请 → 接受 → 角色锁拒绝反向发起 → 双方打洞 → 通过映射通道对 echo 服务做 echo）通过；全仓库 `go vet`/`go test` 通过。


---

## 十九、2026-11-16 设备号补齐与界面去重（v1.3.63）

- **登录即分配**：`registerDevice` 在 Touch/Add 之后调用 `EnsureDeviceNumbers(userID)`，把该账号下所有缺号的设备一次性补 9 位号（老数据也能立刻用），不再要求"让对方重新登录"。
- `p2p.request` 支持 `targetDevice`：设备列表右键「连接到此设备」直接按设备 ID 邀请，不再依赖先拿到设备号；解析到目标后若仍无号会即时补。
- 发起方的设备号缺失时同样即时补齐，邀请里不会出现空的 `#`。
- 前端修复：`设备号穿透` 卡片被 `Replace` 插入两次，删掉重复块（`tsc` 通过）。
- 实测部署后 `wt-admin -devices admire` 三台设备全部有号（924240027 / 418047612 / 113514314）。

---

## 二十、2026-11-16 设置保存死循环 / 本机设备号 / 穿透反馈（v1.3.64）

- **设置保存死循环**：`SettingsPage` 的 `debouncedDownloads` 自动保存 effect 在 `reloadSettings()` 后再次触发，导致每 ~800ms `SaveSettings` 一次（日志刷屏 + 反复写盘）。两层修复：
  - 前端 `saveDownloads` 先比较 `cur.downloads` 与合并结果，相等直接返回；
  - 后端 `App.SaveSettings` 增加 `sameSettings(prev, next)` JSON 比较，**无变化直接返回**，不写文件也不打日志。
- **看不到自己的设备号**：`设备号穿透` 卡片顶部新增「本机设备号 #9位 + 复制」，取自 `p2p.localDevice` 对应设备行。
- **穿透反馈**：`发起穿透` 按钮等待期间显示「等待对方接受…」，并 toast 提示；日志补全 `invite sent / accepted / declined / expired / timed out` 五种状态，不再是只有一条。
- `p2p.request` 支持 `targetDevice`（按设备 ID 邀请）后，设备列表右键连接不再需要设备号；接受方仍必须点「接受」。

---

## 二十一、2026-11-16 本机设备号展示（v1.3.65）

- 之前前端只从 `myDevices` 里按 `p2p.localDevice` 匹配设备行取号，匹配不上（登录设备 id 与设备表 id 不一致 / 状态未加载）就什么都不显示。
- 新增 Go 绑定 **`P2PLocalNumber()`**：直接向同步服务器要 `admin.devices.self`，用本机 `settings.DeviceID()` 匹配，匹配不到再按主机名兜底；返回 `{deviceId, number, name, error}`。
- 设备页「设备号穿透」卡片顶部显示 `本机设备号 #9位 + 复制`；拿不到时显示「本机设备号尚未同步（需要登录同步服务器后刷新）」，不再静默不显示。
- 打洞信令增强：rendezvous 回复带 `start`（双方就绪后 700ms 的同步开始时刻），两端等到该时刻同时发包；探测间隔 200ms→**100ms**，端口预测窗口 ±10→**±20**，1 秒后开始预测，探测期间每 2 秒重新 announce 保持 NAT 映射新鲜。

---

## 二十二、2026-11-16 设备号显示 + 持续探测 + 自动接受（v1.3.66）

### 22.1 为什么界面一直显示"尚未同步"
- 旧代码只按 `p2p.localDevice` 在 `myDevices` 里找设备行；本机登录上报的 id 与设备表里的历史 id 不一致时匹配失败，且**没有任何日志**，界面上就是一句提示。
- 现在 `P2PLocalNumber()`：
  1. 取 `admin.devices.self`；
  2. 打印一行 `p2p: local device id …, host …, account devices [id#number …]`（排查用）；
  3. 依次按 **设备 id → 主机名 → 单设备兜底** 匹配取号；
  4. 拿不到才返回 `error`，前端显示提示而不是空白。

### 22.2 设备号一定存在
- 服务端 `registerDevice` 的两条路径（新增 / 已存在 Touch）**都会**调用 `EnsureDeviceNumbers(userID)`，登录一次即补齐全账号的 9 位号（含历史行）。
- 集成测试新增断言：两台设备登录后 `admin.devices.self` 返回的每一行都必须是 9 位号。

### 22.3 打洞：不再等同步时刻，一直发
- 去掉"等到服务器给的 start 时刻再发包"的门控（`_ = startAt`），两端**从拿到对方地址起持续探测直到超时**，符合"被穿透端开 UDP 端口、穿透端一直发"的思路。
- 探测日志：`p2p: punch socket <本地> -> peer <对端>`，之后每 50 次打印 `p2p: probe #N to <addr> from <local>`，收到回包打印 `p2p: probe answered by … after N probes`。这样日志里能看到 UDP 端口与探测过程。
- 间隔 100ms、1 秒后开始 ±20 端口预测、每 2 秒重新 announce 保持映射。

### 22.4 默认自动接受
- 新增 `ProxyRuntimeSettings.P2PAutoAccept`（默认 `true`）；`Default()` 里置真，旧配置文件缺该键时 `json.Unmarshal` 到 Default 结构上仍是 `true`。
- watcher 收到邀请即 `emit` 并**自动接受**，同时打印 `p2p: auto-accepting invite …`；界面「自动接受穿透」开关可关闭，关闭后必须手动点接受。
- 发起侧加并发保护：同一目标正在打洞时不会重复发起（避免日志里一串 invite）；轮询间隔 2s → 1s。

---

## 二十三、2026-11-16 设备号自愈 + 打洞保活（v1.3.67）

### 23.1 为什么"本机设备号"仍然空着
排查发现服务端 `admin.devices.self` 返回的两行设备里**没有本机那条**：
命令行工具（wt-admin / wt-publish / 临时探针）与桌面端在**同一台机器**上登录时会带上相同的 `machineId`，服务端 `DeleteDevicesByMachine` 把桌面端那行**删掉并替换**成 CLI 的行 —— 桌面端的设备 id 从此在表里不存在，自然没有号。

三处修复：
1. **号不再丢**：`registerDevice` 在按机器指纹替换前，先 `FindDeviceNumberByMachine` 记住原号，注册后 `SetDeviceNumber` 写回 —— 同机器重登号码稳定，不会因为换行而变。
2. **行会自愈**：`admin.devices.self` 接受可选 body `{deviceId, deviceName, machineId}`，若该设备不在表里就当场重新登记。客户端 `P2PLocalNumber()` 每次都带上本机身份，所以打开设备页即可恢复，无需手动重登。
3. **前端持续刷新**：`P2PLocalNumber` 从"进页面取一次"改为**每 5 秒随邀请轮询一起刷新**，取到号立刻显示。

排查日志：`p2p: local device id …, host …, account devices [id#号 …]`，可据此确认匹配结果。

### 23.2 打洞成功之后
- **心跳**：QUIC `KeepAlivePeriod = 10s`，`MaxIdleTimeout` 由 3 分钟放宽到 **10 分钟**，长期空闲也不会被悄悄断开。
- **不依赖同步服务器**：打洞成功后数据走直连 QUIC，只有 `P2PDisconnect`/退出登录才会拆链；服务器断链、重登都不影响已建立的通道（本次实测 `p2p:connected` 后服务器重连，通道保持）。
- **断链自愈**：新增 `Channel.Done()` + `p2pHub.watch`，QUIC 连接一旦消失（换网、对端退出、空闲超时）立即 `drop` 并**释放该设备的全部端口映射**，同时 `emit("p2p:disconnected")`，不会再出现"映射还在但流量黑洞"。

### 23.3 打洞日志（本次实测片段）
```
p2p: invite pr-uzbpcLQm accepted, punching to 6241cc75…
p2p: punch socket 0.0.0.0:52588 -> peer 47.108.84.112:54242
p2p: probe #1 to 47.108.84.112:54242 from 0.0.0.0:52588
p2p: probe answered by 47.108.84.112:54242 after 1 probes
p2p: direct path open via 47.108.84.112:54242, starting QUIC handshake
event p2p:connected
```
同一对设备上一次失败、这次 1 个包就打通 —— 说明之前的"发送一次就停/等同步时刻"确实是主因。

---

## 二十四、2026-11-16 review 后的修正（v1.3.68）

### 24.1 设备号为什么一直看不到（真因）
上一版把号加到 `server.Device`（服务端）与 TS 类型，但**漏了 Wails 绑定层** `internal/app.AdminDevice`（只有 id/name/lastSeen）。前端拿到的 JSON 里根本没有 `number`，所以永远显示空。修：
- `app.AdminDevice` 补 `Number`/`Machine`；
- `MyDevices()` 改为带身份调用 `admin.devices.self`（`deviceId/deviceName/machineId`），服务端据此**自愈**缺失的设备行；
- 前端每 5 秒刷新设备列表 + `P2PLocalNumber()`（此前那个轮询 effect 在删重复卡片时被一并删掉了，导致从未调用过）。

### 24.2 不能连自己
`isSelf` 以前只比设备 id；同一台机器可能有多条行（CLI 登录产生）。现在同时比较 **id / 主机名 / 机器指纹**，`P2PConnect` 也会拒绝"本机的另一条设备记录"。

### 24.3 心跳
新增 `App.heartbeat`：每 **10 秒** 一次 `p2p.ping`，**连续 3 次失败**（30 秒）判定对端失联 → `drop` 通道并释放端口映射，推送 `p2p:disconnected`。QUIC 自身 keepalive 10s、`MaxIdleTimeout` 10 分钟作为兜底。打洞完成后完全不依赖同步服务器。

### 24.4 接受策略
`Request.SameAccount`：**同一账号的设备自动接受**（无需确认），**其它账号的设备必须在界面点「接受」**。`P2PAutoAccept` 开关保留，打开后连他账号的邀请也自动接受。

### 24.5 打洞后的文件管理
- 对端新增 `p2p.fs.list` / `p2p.fs.mkdir` / `p2p.fs.remove`（RPC）与 `p2p.fs.read` / `p2p.fs.write`（流）。
- 路径被限制在对方的**下载目录**内（`filepath.Clean` + 前缀校验，越界拒绝）。
- 客户端新增 `P2PFilesList/Mkdir/Remove/Download/Upload`，界面「设备文件（打洞直连）」卡片：选择已直连设备 → 浏览目录 → 下载到本地 / 上传到对方 / 删除。
- `p2p.capabilities` 现在返回 `{"tcp":true,"files":true,"fsRoot":…}`。
- Channel 新增 `Call`（RPC）与 `OpenStream`（裸流），供文件读写复用。

---

## 二十五、2026-11-16 打洞"通了马上断"（v1.3.69）

### 25.1 现象
```
p2p: probe answered by 47.108.84.112:55221 after 1 probes
p2p: direct path open via 47.108.84.112:55221, starting QUIC handshake
p2p: heartbeat … failed (1/3): deadline exceeded   ← 第一次心跳就失败
```
QUIC 握手成功（否则不会启动心跳），但之后所有新流（心跳 ping）都超时。

### 25.2 结论与修复
1. **NAT 映射过期**是主因：打洞成功后探测流量停止，很多 CGNAT/对称 NAT 在几秒内回收映射，QUIC 的默认 10 秒 keepalive 太慢。
   → `KeepAlivePeriod = 3 * time.Second`（`MaxIdleTimeout` 仍 10 分钟），从握手完成就开始持续打点保持映射。
2. **不要先报"已连接"再失败**：新增 `verifyChannel`，通道建立后立刻做一次 `p2p.ping`（12s 超时），失败则直接报「打洞成功但链路无响应（NAT 映射可能已关闭）」并 drop，而不是先弹"直连成功"再静默超时。
3. 心跳超时从 8s 放宽到 15s（间隔 10s），失败日志附带 `quic alive=true/false`，用于区分"RPC 不通"与"连接已死"。
4. 新增 `Channel.Alive()`；`TestChannelRPC`（集成测试）验证打洞通道上的 RPC（心跳路径）可用。

### 25.3 排查建议
- 若日志出现 `link to … opened but does not answer`：NAT 映射在握手后立刻失效，属于链路问题（换 5G/家庭宽带对比）。
- 若 `quic alive=false`：连接已被 QUIC 判定死亡（空闲超时或对端关闭）。

---

## 二十六、2026-11-16 打洞后必然超时的真因：verify 死锁（v1.3.70）

### 26.1 现象（双方日志对照）
两端都打印：
```
p2p: probe answered ... after 1 probes      ← 打洞成功
p2p: direct path open ..., starting QUIC handshake
p2p: link to … opened but does not answer (deadline exceeded)   ← 15s 后
```
即 QUIC 握手完成，但第一个 RPC（`p2p.ping`）就超时，两边都一样。

### 26.2 根因
v1.3.69 加的"打通后先校验链路"逻辑放在了 `go channel.Serve(...)` **之前**：

```go
a.p2p.put(...)
if !a.verifyChannel(...) { return }        // 需要对方 Serve 才能应答 ping
go channel.Serve(...)                      // 但自己的 Serve 还没起
```
于是**双方都在等对方先启动 Serve 才回应 ping**，而各自的 Serve 都在 ping 之后才启动 —— 互相等待 12 秒后同时判超时、同时断开。打洞本身没有任何问题，是被自己的校验逻辑锁死了。

### 26.3 修复
两端（`answerPunch` 与 `p2pConnectDevice`）都改为：**先 `go channel.Serve(...)`，再 `verifyChannel`**，最后才启动心跳与 watcher。

### 26.4 顺带观察
对端日志里我们的公网 IP 在 `218.201.253.88` / `218.201.253.98` 之间变化 —— 属于运营商 NAT 池，按流分配；不同尝试用不同源端口所以 IP 不同，属正常现象，不是本次故障原因（同一条流内映射是稳定的）。

---

## 三十九、2026-11-16 基于 v1.3.70 重做（v1.3.71）

### 39.1 基线
`git reset --hard 4b4dc58`（v1.3.70）。此前所有实验性改动保存在分支 `keep/v1.3.81-relay` 与标签 `v1.3.83-relay`。

### 39.2 打洞：恢复"热身 + 互相打洞"
成功日志（20:22）显示：
```
direct dial to 47.108.84.112:53937 failed (context deadline exceeded), switching to mutual punching
probe #1 ... → probe answered after 1 probes            ← 第一个包就通了
```
原因：那次"假设对方有公网"的 QUIC 尝试**在双方 NAT 上都开了映射**，所以接下来第一个探测包就能互换。这是一次有效的**热身**，不是浪费。
因此 `Punch` = 热身直拨（`DirectTimeout`，默认 5 秒）→ `drainUDP` 清残留 → 同时探测 + ±20 端口预测（总预算 15 秒）→ QUIC 握手（KeepAlive 3 秒）。

### 39.3 中转重写
- 服务端 `p2p.bridge`（`server/internal/api/bridge.go`）重写：目标设备 park 一条流，发起方取走后**由发起方独占双向拷贝**，结束即关闭双方；`0x01/0x00` 单字节握手；park TTL 10 分钟，重复注册替换旧流。
- 客户端 `internal/p2p/relay.go` 重写：`RelayDial` / `RelayServe`，握手字节单独校验；yamux `LogOutput=io.Discard`（必须设置，否则启动报错）；`BridgeSecret(account)` 按**账号**派生，两端一致（用设备号会 bad channel token）。
- 通道抽象：`internal/p2p/session.go`（`QuicSession` / `YamuxSession`），`Channel` 不再绑定 quic，打洞与中转共用同一套 `DialTCP/Call/OpenStream/Serve`。

### 39.4 右键菜单重做
设备行只保留 `✕`（移除）；右键菜单顺序：**打洞直连 / 断开直连 → 经服务器中转（延迟更高） → 复制设备号 → 复制设备 ID → 移除该设备**（移除标红）。

### 39.5 从后续版本 cherry-pick 的功能
- 百度网盘盘内下载：桌面 UA `netdisk;7.44.7.1;PC;PC-Windows;10.0.19045;WindowsBaiduYunGuanJia`、dlink 302 跟随、**timestamp 不再被格式化成科学计数法**（errno 113 的根因）、列表类接口用浏览器 UA（两者必须分开）。
- 服务端离线下载支持 **URL + 任意 Header**（`offline.add` 接受 `url`/`headers`），落 `离线下载/` 并计入 10 GiB 配额；网盘右键「推送到服务器离线下载」。
- 订阅 UA 可选（浏览器默认 / Quantumult X / Clash / v2rayN / Surge / Shadowrocket / 自定义），支持单订阅覆盖。

---

## 四十、2026-11-16 百度网盘下载 403（v1.3.72）

### 40.1 实测结论
用真实账号跑了三组实验：

| 请求 | 结果 |
|---|---|
| 不带 Range 的普通 GET | **有时 403**（CDN 反盗链） |
| `Range: bytes=0-0 / 0-1 / 0-1023 / 0-65535` | **全部 206** |
| 直连 vs 走本机代理 | 完全一样（说明与出口 IP 无关） |
| 同一 URL 连续多次 Range 请求 | 都正常 |

失败任务的日志：`8 连接 · direct · 重试 24 · chunk 2 failed after retries: unexpected status 403 Forbidden`
→ **多连接并发**才是主因：bd 的签名 URL 有并发限制，多余连接被 403。

### 40.2 修复
1. **百度 CDN 强制单连接**：`NetdiskEnqueueDownload` 里 `isBaiduCDN(url, referer)` 命中（`baidupcs.com` / `pcs.baidu.com` / `pan.baidu.com`）时 `Connections = 1`；夸克/UC 仍是 8。
2. **探测始终带 Range**（`bytes=0-0`，遇 403 再试 `bytes=0-65535`），不再退回无 Range 的普通 GET。
3. `statusError.retryable()` 把 **403** 也算可重试（CDN 反盗链是间歇性的）。
4. 引擎兜底：某分片收到 403 且 `Connections > 1` 时，**自动降为单连接**重试，并在任务上提示"服务端拒绝并发连接（403），已切换为单连接重试"。

---

## 四十一、2026-11-16 百度网盘 403 真因：Range 超过 4 MiB（v1.3.73）

### 41.1 决定性实验
用真实账号对同一个 804 MB 文件的签名直链做对照（UA=`netdisk;7.44.7.1;…`、带 Cookie/Referer）：

| 单个 Range 大小 | 结果 |
|---|---|
| 1 MiB | 206 ✅ |
| 4 MiB | 206 ✅ |
| **8 MiB** | **403 ❌** |
| 16 / 32 / 64 MiB | 403 ❌ |

并发本身没问题：8 条 4 MiB 的并行 Range 全部 206（跑了两轮）。
而引擎默认分片是 **32 MiB**（设置里的 `chunkSizeBytes`），于是每个分片请求都被 403。实测引擎路径：
- 32 MiB 分片：`retries=32, downloaded=0`
- 4 MiB 分片：`retries=0, downloaded=2.5 MB/8s`（正常下载）

### 41.2 修复
- `dlengine.Request/Task` 新增 `ChunkSize`（每任务覆盖全局）。
- `NetdiskEnqueueDownload` 对百度 CDN（`baidupcs.com` / `pcs.baidu.com` / `pan.baidu.com`）设置 **4 MiB** 分片，连接数保持 8。
- 之前的猜测（单连接、UA 缺失）都已用实验排除：UA 经 `TestHeadersReachTheOrigin` 证明会原样送达；并发 8 条 4 MiB 正常。

### 41.3 保留的健壮性
- 探测始终带 `Range`；`If-Range` 只在续传分片内部时才发（CDN 不喜欢无谓的 If-Range）。
- 403 计入可重试，并在并发 > 1 且收到 403 时自动降为单连接（兜底）。
- `WT_DL_DEBUG=1` 可打印每个分片请求的 URL/Range/UA（排查用，默认静默）。

---

## 四十二、2026-11-16 设备文件归位 / 目录输入 / 记住选中订阅（v1.3.75）

- **设备文件**从「同步服务」移到「文件 → 设备文件」标签（新组件 `frontend/src/pages/DeviceFilesCard.tsx`：设备下拉 200px + 路径输入 `flex:1; minWidth:260`，回车即跳转，支持 `D:\Media` 之类盘符；上传/下载/删除/刷新）。
- **本地文件**工具栏新增宽输入框（`minWidth:320`），输入目录回车直接跳转，不再是只能点面包屑。
- **订阅页记住上次选中的订阅**：`localStorage["wt.subs.selected"]`，加载时优先恢复（列表里仍存在才用），点击切换时写入。
- 记一笔教训：`FilesPage` 有两个 `{tab === 'transfers' && …}` 块，两次字符串替换各命中一处，导致 JSX 结构被破坏；此类多次命中的锚点必须先 `grep` 确认唯一，或改用精确的整块替换。

---

## 四十三、2026-11-16 SSH 工作区重做 + 自定义节点被清空的修复（v1.3.76）

### 43.1 SSH 布局
- 去掉「文件 / 终端 / 命令」标签页，改成**左文件、右终端**的两栏网格（`.ssh-split`，窄屏自动上下叠）。
- 两个面板**同时挂载**：切到别的页面再回来，终端不再重新建 PTY（终端只依赖远端会话，缓冲区不丢）。
- 删除「命令」标签页（`CommandPane` 保留但不再挂载）。

### 43.2 远端系统信息条
新增 `internal/sshmgr/sysinfo.go`：一次 `sh` 脚本取 `hostname`、`/proc/cpuinfo` 型号与核数、`/proc/stat` 两次采样算 CPU 使用率、`/proc/meminfo` 算内存、`/proc/loadavg`、`/proc/uptime`；若存在 `nvidia-smi` 再取 `name,utilization,memory.used,total,temperature`。
绑定 `SSHSystemInfo(sessionId)`，前端 `SystemInfoBar` 每 5 秒刷新，顶部一行显示：主机、CPU 型号与核数、使用率、内存、负载、运行时长、GPU 列表。

### 43.3 文件与终端目录同步
- 终端打开后注入 `PROMPT_COMMAND` 让 shell 输出 **OSC 7**（`file://host/path`），前端用 `parser.registerOscHandler(7)` 解析并回调父组件；同时 5 秒轮询 `SSHCwd` 兜底（读 `/proc/<pid>/cwd`）。
- 文件面板收到目录变化就自动刷新到同一目录。

### 43.4 文件操作
- **拖拽上传**：`onDrop` 读取 `DataTransfer` 里的绝对路径（Wails WebView 会带上），逐个 `SFTPUpload`。
- **双击预览**：目录进入、文件直接打开预览（原本就有，保留）。
- **右键删除**：`onContextMenu` 选中并弹出删除确认。

### 43.5 自定义节点被清空（顺手修的 bug）
现象：自定义节点页显示 0，实际 `%LOCALAPPDATA%\WailsTools\nodes\custom-nodes.json` 里 3 个节点（1 内置 + 2 自定义）。
复现：用 `wt-nodecheck` 读同一个文件，`OpenNodes` 只返回 1 个 —— 文件在应用运行期间被**重写成只剩内置节点**。
修复：`NodeStore.persist()` 加安全网 —— 如果内存里已没有自定义节点、而磁盘上还有，就沿用磁盘内容，避免一次空列表同步把用户节点抹掉。用户的 2 个节点（trojan `134.175.90.14:4443`、vmess `111:443`）已按原样写回。

---

## 四十四、2026-11-16 下载引擎单请求硬上限 4 MiB（v1.3.78）

### 44.1 为什么 v1.3.73 之后还报 403
v1.3.73 只给**新任务**设置了 4 MiB 分片；已经存在的任务（旧的 32 MiB 分片计划）继续续传，仍然发 32 MiB 的 Range → 依旧 403（日志里 `chunk 7 failed ... 403`）。

### 44.2 修复
在 `fetchChunkOnce` 里加了**与分片计划无关的硬上限**：

```go
to := c.End
if to-from+1 > maxRangeBytes { to = from + maxRangeBytes - 1 }   // maxRangeBytes = 4 MiB
```
每个 HTTP 请求最多 4 MiB，超过的部分由下一次调用继续取（`deadlineBytes` 控制本次只读到 `to`）。这样：
- 新任务、旧任务、断点续传，**任何**情况下单个 Range 都不会超 4 MiB；
- 分片大小只影响单文件并发度和状态粒度，不再影响 403。

### 44.3 验证
把测试分片故意设成 32 MiB（模拟旧任务）：

| 分片 | 结果 |
|---|---|
| 32 MiB（v1.3.73 会 403） | `retries=0, downloaded=2.9 MB / 9s` ✅ |

真实 804 MB 文件、8 连接、netdisk UA、Cookie/Referer 全部照旧。

---

## 四十五、2026-11-16 SSH 三处体验修正（v1.3.79）

### 45.1 主机列表自动收缩
`.ssh-root.session-open .ssh-hosts` → 连接后左栏收成 64px 窄条，**鼠标移上去**（或 focus）展开到 340px 并浮在工作区上方（`position: relative; z-index`），移到别处又收回去；断开连接后（`sessionId` 为空）恢复常驻。
窄条状态下内部控件 `opacity: 0; pointer-events: none`，避免误点。

### 45.2 主机占用信息到底部
`SystemInfoBar` 从工作区上方移到下方，加 `.ssh-sysinfo { position: sticky; bottom: 0 }`，滚动时也固定可见；这样文件列表和终端可以占满剩余高度。

### 45.3 采样不丢数据
- 采集失败时**保留上一次成功的样本**继续显示，并在主机名后标注"更新中断，显示上一次数据"，避免整条信息条突然变空。
- 后端 `SSHSystemInfo` 增加一次 300ms 后的重试（`ssh exec` 在高负载下偶发超时）。
- 前端 1 秒轮询带 `busyRef` 互斥，慢主机不会堆积请求。

### 45.4 顺手清掉一个 bug
上一版为了让文件面板跟随终端目录，往 shell 里注入了 `PROMPT_COMMAND`，但转义没处理好，命令**原样漏进了终端**（截图里那行 `export PROMPT_COMMAND='printf "\033]7;...`）。已删除该注入：现在只依赖 5 秒一次的 `SSHCwd` 轮询 + 若 shell 自己发 OSC 7 也能解析。

---

## 四十六、2026-11-16 SSH 再修一轮（v1.3.80）

### 46.1 收缩没生效的原因
上一版只改了子元素 `width`，但父级是 `grid-template-columns: 320px 1fr`，**列宽由父级决定**，子元素怎么改都没用。
改用 Pulse 的做法（`frontend/src/App.css` 里 `--sidebar-width` + `:has()`）：
```css
.ssh-root.session-open { grid-template-columns: 64px minmax(0,1fr); transition: grid-template-columns .2s; }
.ssh-root.session-open:has(.ssh-hosts:hover),
.ssh-root.session-open:has(.ssh-hosts:focus-within) { grid-template-columns: 340px minmax(0,1fr); }
```

### 46.2 占用信息到底部
上一版替换锚点没命中，`<SystemInfoBar/>` 实际还在工作区上方。这次直接改结构：split 之后才是 `<SystemInfoBar/>`，并加 `.ssh-sysinfo { position: sticky; bottom: 0 }`。

### 46.3 目录同步方向反过来
以**文件浏览器为准**：`SftpPane` 的 `dir` 变化时通过 `onDir` 通知父级，`TerminalPane` 收到 `cwd` 就向 shell 发 `cd '<path>'`（单引号转义，空格安全）。之前是终端驱动文件，容易因为轮询延迟打架。

### 46.4 静态信息不闪
主机名、CPU 型号、核数永不变化：采样返回空值时**沿用上一次的值**（`setInfo(prev => ({...next, hostname: next.hostname || prev.hostname, ...}))`）。

### 46.5 磁盘占用
只取根分区：`df -P -k / | awk 'NR==2{print $2" "$3" "$5}'`，解析成 `diskTotalMb/diskUsedMb/diskPct`，前端多一条「磁盘 /」进度条（不逐盘列出）。

### 46.6 终端右键粘贴
xterm.js 默认不处理右键。给 `.term-xterm` 加 `onContextMenu` → `navigator.clipboard.readText()` → 走 `WriteTerminal`（与输入同一通道）；WebView 拒绝剪贴板权限时提示改用 `Ctrl+Shift+V`。

---

## 四十七、2026-11-16 SSH 缓存 + 右键复制 + 拖拽不跳转（v1.3.81）

### 47.1 静态信息在 Go 侧缓存
前端合并还不够 —— 采样本身可能整段失败。现在 `internal/app/ssh_sysinfo.go` 按 `sessionID` 缓存上一次成功结果：
- `hostname` / `cpuModel` / `cpuCores` **只取一次**，后续只在为空时用缓存补；
- 整段采样失败（`MemTotalMB==0 && CPUModel=="" && Hostname==""`）直接返回上次快照；
- 内存、磁盘为 0 时也回填上次值，避免信息条"跳空"。

### 47.2 终端右键：有选中就复制，否则粘贴
`onContextMenu` 里先看 `term.getSelection()`：有选中 → `navigator.clipboard.writeText` 并清除选区 + 提示"已复制"；没有选中 → 走粘贴逻辑。

### 47.3 拖拽不再变成下载
把文件拖进 WebView 默认会导航（看起来就像开始下载）。在 `main.tsx` 加了全局：
```js
window.addEventListener('dragover', (e) => e.preventDefault())
window.addEventListener('drop', (e) => e.preventDefault())
```
SSH 的投放区再自己 `preventDefault + stopPropagation`，并只在能拿到 `file.path` 时上传；拿不到就提示改用「上传」按钮（WebView2 有时不暴露路径）。

---

## 四十八、2026-11-16 终端右键菜单 / 磁盘与主机信息兜底（v1.3.82）

### 48.1 右键改成显式菜单
上一版"有选中就复制、否则粘贴"的猜测在 WebView 里失败（`getSelection()` 偶尔返回残留内容，导致右键永远在复制）。改成在光标处弹出小菜单：

- **复制（选中）** —— `term.getSelection()` 有内容时复制，没有则提示"先选中要复制的文字"；
- **粘贴** —— 读剪贴板后 `WriteTerminal`。

菜单用现成的 `.ctx-menu / .ctx-item` 样式，`position: fixed`，鼠标离开即关闭。

### 48.2 磁盘占用没显示
原来把 `df` 塞进那条大 `sh -c` 里，用 `awk` 拼字段，任何一步出错整段没有输出。改为**独立的一次 Exec**：

```
df -P -k / | tail -n1
```
前端解析第二、三、五列（1K-blocks / used / capacity）。在 s2 上实测：
```
/dev/vda2  51485392 29450136 19817144 60% /
```

### 48.3 主机名 / CPU 型号为空
部分精简镜像里 `hostname`、`grep model name` 不可用或没权限。现在主脚本失败后**各字段单独兜底**：

- 主机名：`uname -n || cat /etc/hostname`
- CPU：`lscpu | sed -n 's/^Model name:[[:space:]]*//p'`

再加上会话级缓存（47.1），成功取到一次后就不会再显示"主机 —、CPU 未知"。
