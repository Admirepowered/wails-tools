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
