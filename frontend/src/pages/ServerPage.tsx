import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, onEvent, type RemoteEntry, type ServerStatus, type Settings } from '../lib/api'
import { Confirm, Empty, Field, Modal, Progress, Spinner, Switch, Tabs, Tag, useToast } from '../components/ui'
import { formatBytes, formatTime, percent } from '../lib/format'
import type { PageProps } from '../App'

type AuthTab = 'login' | 'register'

interface CloudTransfer {
  key: string
  done: number
  total: number
}

function joinRemote(base: string, name: string): string {
  const trimmed = (base || '').replace(/\/+$/, '')
  return `${trimmed}/${name}`.replace(/\/{2,}/g, '/')
}

function transportView(transport?: string, connected?: boolean) {
  const t = (transport ?? '').toLowerCase()
  if (!connected) return <Tag>未连接</Tag>
  if (t === 'udp') return <Tag kind="ok">UDP · QUIC 优先</Tag>
  if (t === 'tcp') return <Tag kind="info">TCP 回退</Tag>
  return <Tag>{transport || '未知传输'}</Tag>
}

export default function ServerPage(props: PageProps) {
  const toast = useToast()

  // endpoint form
  const [host, setHost] = useState('127.0.0.1')
  const [port, setPort] = useState(8642)
  const [preferUdp, setPreferUdp] = useState(true)
  const [disableUdp, setDisableUdp] = useState(false)
  const [autoConnect, setAutoConnect] = useState(false)
  const [saving, setSaving] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [loggingOut, setLoggingOut] = useState(false)

  // status
  const [localStatus, setLocalStatus] = useState<ServerStatus | null>(null)

  // auth
  const [authTab, setAuthTab] = useState<AuthTab>('login')
  const [loginUser, setLoginUser] = useState('')
  const [loginPass, setLoginPass] = useState('')
  const [loginBusy, setLoginBusy] = useState(false)

  const [regUser, setRegUser] = useState('')
  const [regPass, setRegPass] = useState('')
  const [regPass2, setRegPass2] = useState('')
  const [regEmail, setRegEmail] = useState('')
  const [regDisplay, setRegDisplay] = useState('')
  const [regBusy, setRegBusy] = useState(false)

  const [pwdOld, setPwdOld] = useState('')
  const [pwdNew, setPwdNew] = useState('')
  const [pwdNew2, setPwdNew2] = useState('')
  const [pwdBusy, setPwdBusy] = useState(false)

  // cloud storage
  const [cloudPath, setCloudPath] = useState('/')
  const [entries, setEntries] = useState<RemoteEntry[]>([])
  const [cloudLoading, setCloudLoading] = useState(false)
  const [selectedEntry, setSelectedEntry] = useState<RemoteEntry | null>(null)
  const [mkdirOpen, setMkdirOpen] = useState(false)
  const [mkdirName, setMkdirName] = useState('')
  const [mkdirBusy, setMkdirBusy] = useState(false)
  const [removeTarget, setRemoveTarget] = useState<RemoteEntry | null>(null)
  const [uploadBusy, setUploadBusy] = useState(false)
  const [transfers, setTransfers] = useState<CloudTransfer[]>([])
  const [usage, setUsage] = useState<{ usedBytes: number; quotaBytes: number } | null>(null)

  // relay
  const [relay, setRelay] = useState<{ available: boolean; detail: string } | null>(null)

  const synced = useRef(false)
  const status = props.serverStatus ?? localStatus
  const connected = status?.connected === true
  const authed = !!status?.user
  const user = status?.user

  /* ---------------- settings sync ---------------- */

  const applyServerSettings = useCallback((server: Settings['server']) => {
    setHost(server.host || '127.0.0.1')
    setPort(server.port || 8642)
    setPreferUdp(!!server.preferUdp)
    setDisableUdp(!!server.disableUdp)
    setAutoConnect(!!server.autoConnect)
  }, [])

  useEffect(() => {
    const onExpired = (ev: Event) => {
      const detail = (ev as CustomEvent<string>).detail
      toast.push(detail ? `登录已失效，请重新登录（${detail}）` : '登录已失效，请重新登录', 'warn')
    }
    window.addEventListener('wails-tools:login-expired', onExpired)
    return () => window.removeEventListener('wails-tools:login-expired', onExpired)
  }, [toast])

  useEffect(() => {
    if (synced.current || !props.settings?.server) return
    synced.current = true
    applyServerSettings(props.settings.server)
  }, [props.settings, applyServerSettings])

  /* ---------------- data loading ---------------- */

  const loadStatus = useCallback(async () => {
    try {
      setLocalStatus(await api.ServerStatus())
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const loadRelay = useCallback(async () => {
    try {
      setRelay(await api.RelayStatus())
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const loadUsage = useCallback(async () => {
    try {
      setUsage(await api.ServerUsage())
    } catch {
      setUsage(null)
    }
  }, [])

  const loadCloud = useCallback(
    async (target: string) => {
      setCloudLoading(true)
      try {
        const list = await api.ListServerFiles(target)
        setEntries(list)
        setSelectedEntry(null)
      } catch (err) {
        toast.error(err)
        setEntries([])
      } finally {
        setCloudLoading(false)
      }
    },
    [toast],
  )

  useEffect(() => {
    const onExpired = (ev: Event) => {
      const detail = (ev as CustomEvent<string>).detail
      toast.push(detail ? `登录已失效，请重新登录（${detail}）` : '登录已失效，请重新登录', 'warn')
    }
    window.addEventListener('wails-tools:login-expired', onExpired)
    return () => window.removeEventListener('wails-tools:login-expired', onExpired)
  }, [toast])

  useEffect(() => {
    void loadStatus()
    void loadRelay()
  }, [loadStatus, loadRelay])

  useEffect(() => {
    const onExpired = (ev: Event) => {
      const detail = (ev as CustomEvent<string>).detail
      toast.push(detail ? `登录已失效，请重新登录（${detail}）` : '登录已失效，请重新登录', 'warn')
    }
    window.addEventListener('wails-tools:login-expired', onExpired)
    return () => window.removeEventListener('wails-tools:login-expired', onExpired)
  }, [toast])

  useEffect(() => {
    const off = onEvent<ServerStatus>('server:status', (st) => {
      if (st) setLocalStatus(st)
    })
    return () => off()
  }, [])

  useEffect(() => {
    const onExpired = (ev: Event) => {
      const detail = (ev as CustomEvent<string>).detail
      toast.push(detail ? `登录已失效，请重新登录（${detail}）` : '登录已失效，请重新登录', 'warn')
    }
    window.addEventListener('wails-tools:login-expired', onExpired)
    return () => window.removeEventListener('wails-tools:login-expired', onExpired)
  }, [toast])

  useEffect(() => {
    // Cloud storage needs a logged-in account, not just a live transport.
    if (!connected || !authed) {
      setEntries([])
      setUsage(null)
      return
    }
    void loadCloud(cloudPath)
    void loadUsage()
  }, [connected, authed, cloudPath, loadCloud, loadUsage])

  const refreshCloud = useCallback(() => {
    void loadCloud(cloudPath)
    void loadUsage()
  }, [cloudPath, loadCloud, loadUsage])

  const refreshCloudRef = useRef<() => void>(() => {})
  useEffect(() => {
    const onExpired = (ev: Event) => {
      const detail = (ev as CustomEvent<string>).detail
      toast.push(detail ? `登录已失效，请重新登录（${detail}）` : '登录已失效，请重新登录', 'warn')
    }
    window.addEventListener('wails-tools:login-expired', onExpired)
    return () => window.removeEventListener('wails-tools:login-expired', onExpired)
  }, [toast])

  useEffect(() => {
    refreshCloudRef.current = refreshCloud
  }, [refreshCloud])

  useEffect(() => {
    const onExpired = (ev: Event) => {
      const detail = (ev as CustomEvent<string>).detail
      toast.push(detail ? `登录已失效，请重新登录（${detail}）` : '登录已失效，请重新登录', 'warn')
    }
    window.addEventListener('wails-tools:login-expired', onExpired)
    return () => window.removeEventListener('wails-tools:login-expired', onExpired)
  }, [toast])

  useEffect(() => {
    const offProgress = onEvent<{ key?: string; kind?: string; done?: number; total?: number }>(
      'transfer:progress',
      (ev) => {
        if (ev?.kind !== 'cloud-upload' || !ev.key) return
        const key = ev.key
        const item: CloudTransfer = {
          key,
          done: typeof ev.done === 'number' ? ev.done : 0,
          total: typeof ev.total === 'number' ? ev.total : 0,
        }
        setTransfers((prev) => {
          const idx = prev.findIndex((t) => t.key === key)
          if (idx === -1) return [...prev, item]
          const next = [...prev]
          next[idx] = item
          return next
        })
      },
    )
    const offDone = onEvent<{ key?: string; kind?: string; path?: string }>('transfer:done', (ev) => {
      if (ev?.kind !== 'cloud-upload') return
      setTransfers((prev) => prev.filter((t) => t.key !== ev.key))
      refreshCloudRef.current()
    })
    return () => {
      offProgress()
      offDone()
    }
  }, [])

  /* ---------------- endpoint actions ---------------- */

  const saveEndpoint = async () => {
    const cleanHost = host.trim() || '127.0.0.1'
    const cleanPort = Number.isFinite(port) && port > 0 ? Math.min(65535, Math.round(port)) : 8642
    setSaving(true)
    try {
      const saved = await api.SaveServerEndpoint(cleanHost, cleanPort, preferUdp, disableUdp)
      applyServerSettings(saved.server)
      await props.reloadSettings()
      toast.push('服务器地址已保存')
    } catch (err) {
      toast.error(err)
    } finally {
      setSaving(false)
    }
  }

  const toggleAutoConnect = async (value: boolean) => {
    setAutoConnect(value)
    if (!props.settings) return
    try {
      await api.SaveSettings({ ...props.settings, server: { ...props.settings.server, autoConnect: value } })
      await props.reloadSettings()
      toast.push(value ? '启动时自动连接已开启' : '启动时自动连接已关闭')
    } catch (err) {
      setAutoConnect(!value)
      toast.error(err)
    }
  }

  const connect = async () => {
    setConnecting(true)
    try {
      const st = await api.ConnectServer()
      setLocalStatus(st)
      await props.refreshServerStatus()
      toast.push((st.transport ?? '').toLowerCase() === 'udp' ? '已通过 UDP / QUIC 连接服务器' : '已连接服务器（TCP）')
      void loadRelay()
    } catch (err) {
      toast.error(err)
    } finally {
      setConnecting(false)
    }
  }

  const logout = async () => {
    setLoggingOut(true)
    try {
      await api.LogoutServer()
      await props.refreshServerStatus()
      await loadStatus()
      setPwdOld('')
      setPwdNew('')
      setPwdNew2('')
      toast.push('已退出登录，本地令牌已清除')
    } catch (err) {
      toast.error(err)
    } finally {
      setLoggingOut(false)
    }
  }

  /* ---------------- auth actions ---------------- */

  const checkPasswordLength = (pwd: string): boolean => {
    if (pwd.length < 8) {
      toast.push('密码长度至少为 8 位', 'warn')
      return false
    }
    return true
  }

  const doLogin = async () => {
    if (!loginUser.trim()) {
      toast.push('请输入用户名', 'warn')
      return
    }
    if (!checkPasswordLength(loginPass)) return
    setLoginBusy(true)
    try {
      const st = await api.LoginServer(loginUser.trim(), loginPass)
      setLocalStatus(st)
      await props.refreshServerStatus()
      setLoginPass('')
      toast.push(`已登录：${st.user?.username ?? loginUser.trim()}`)
      void loadUsage()
    } catch (err) {
      toast.error(err)
    } finally {
      setLoginBusy(false)
    }
  }

  const doRegister = async () => {
    const name = regUser.trim()
    if (!name) {
      toast.push('请输入用户名', 'warn')
      return
    }
    if (!checkPasswordLength(regPass)) return
    if (regPass !== regPass2) {
      toast.push('两次输入的密码不一致', 'warn')
      return
    }
    setRegBusy(true)
    try {
      const st = await api.RegisterServer(name, regPass, regEmail.trim(), regDisplay.trim())
      setLocalStatus(st)
      await props.refreshServerStatus()
      setRegPass('')
      setRegPass2('')
      toast.push('注册成功，已自动登录')
      void loadUsage()
    } catch (err) {
      toast.error(err)
    } finally {
      setRegBusy(false)
    }
  }

  const doChangePassword = async () => {
    if (!pwdOld) {
      toast.push('请输入当前密码', 'warn')
      return
    }
    if (!checkPasswordLength(pwdNew)) return
    if (pwdNew !== pwdNew2) {
      toast.push('两次输入的新密码不一致', 'warn')
      return
    }
    setPwdBusy(true)
    try {
      await api.ChangeServerPassword(pwdOld, pwdNew)
      setPwdOld('')
      setPwdNew('')
      setPwdNew2('')
      toast.push('密码已修改')
    } catch (err) {
      toast.error(err)
    } finally {
      setPwdBusy(false)
    }
  }

  /* ---------------- cloud actions ---------------- */

  const crumbs = useMemo(() => {
    const parts = (cloudPath || '/').split('/').filter(Boolean)
    let acc = ''
    return parts.map((part) => {
      acc += `/${part}`
      return { label: part, path: acc }
    })
  }, [cloudPath])

  const goCloud = (target: string) => {
    setCloudPath(target || '/')
    setSelectedEntry(null)
  }

  const doMkdir = async () => {
    const name = mkdirName.trim()
    if (!name) {
      toast.push('请输入文件夹名称', 'warn')
      return
    }
    setMkdirBusy(true)
    try {
      await api.MkdirServerFile(joinRemote(cloudPath, name))
      setMkdirOpen(false)
      setMkdirName('')
      toast.push('文件夹已创建')
      refreshCloud()
    } catch (err) {
      toast.error(err)
    } finally {
      setMkdirBusy(false)
    }
  }

  const uploadToCloud = async () => {
    setUploadBusy(true)
    try {
      const local = await api.PickFile('选择要上传到云端存储的文件')
      if (!local) return
      const res = await api.UploadToServer([local], cloudPath)
      const results = Array.isArray(res?.results) ? res.results : []
      const failed = results.find((item) => typeof item?.error === 'string' && item.error !== '')
      if (failed) {
        toast.push(`部分文件上传失败：${String(failed.error)}`, 'warn')
      } else {
        toast.push('上传完成')
      }
      refreshCloud()
    } catch (err) {
      toast.error(err)
    } finally {
      setUploadBusy(false)
    }
  }

  const downloadFromCloud = async () => {
    const entry = selectedEntry
    if (!entry || entry.isDir) {
      toast.push('请先选择一个文件', 'warn')
      return
    }
    try {
      const dir = await api.PickDirectory('选择保存目录')
      if (!dir) return
      const res = await api.DownloadFromServer(entry.path, dir)
      toast.push(`已下载到 ${res.path || dir}`)
    } catch (err) {
      toast.error(err)
    }
  }

  const removeEntry = async () => {
    const entry = removeTarget
    if (!entry) return
    setRemoveTarget(null)
    try {
      await api.RemoveServerFile(entry.path, entry.isDir)
      toast.push('已删除')
      refreshCloud()
    } catch (err) {
      toast.error(err)
    }
  }

  /* ---------------- render ---------------- */

  const udpDisabled = disableUdp

  return (
    <div className="page-narrow">
      <div className="card">
        <h3 className="card-title">服务器地址</h3>
        <p className="card-desc">
          同步服务优先使用 UDP（基于 QUIC 的低延迟传输），网络受限时自动回退到 TCP。修改地址后需重新连接。
        </p>
        <div className="grid-4">
          <Field label="主机地址">
            <input
              className="input mono"
              value={host}
              placeholder="127.0.0.1"
              onChange={(e) => setHost(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && void saveEndpoint()}
            />
          </Field>
          <Field label="端口">
            <input
              className="input mono"
              type="number"
              min={1}
              max={65535}
              value={port}
              onChange={(e) => setPort(Number(e.target.value) || 0)}
            />
          </Field>
          <div className="field">
            <label>优先 UDP / QUIC</label>
            <div className="row" style={{ minHeight: 36 }}>
              <Switch
                checked={preferUdp && !udpDisabled}
                title="优先使用 UDP / QUIC"
                onChange={(v) => {
                  setPreferUdp(v)
                  if (v) setDisableUdp(false)
                }}
              />
              <span className="small faint">{preferUdp && !udpDisabled ? '已开启' : '已关闭'}</span>
            </div>
          </div>
          <div className="field">
            <label>禁用 UDP，强制 TCP</label>
            <div className="row" style={{ minHeight: 36 }}>
              <Switch
                checked={disableUdp}
                title="禁用 UDP，强制走 TCP"
                onChange={(v) => {
                  setDisableUdp(v)
                  if (v) setPreferUdp(false)
                }}
              />
              <span className="small faint">{disableUdp ? '强制 TCP' : '允许 UDP'}</span>
            </div>
          </div>
        </div>

        <div className="divider" />

        <div className="row row-wrap">
          <div className="row" style={{ gap: 8 }}>
            <Switch checked={autoConnect} onChange={(v) => void toggleAutoConnect(v)} title="启动应用时自动连接" />
            <span className="small dim">启动时自动连接</span>
          </div>
          <div className="spacer" />
          <button className="btn" onClick={() => void saveEndpoint()} disabled={saving}>
            {saving ? '保存中…' : '保存'}
          </button>
          <button className="btn btn-primary" onClick={() => void connect()} disabled={connecting}>
            {connecting ? '连接中…' : '连接'}
          </button>
          <button className="btn" onClick={() => void logout()} disabled={loggingOut}>
            {loggingOut ? '退出中…' : '退出登录'}
          </button>
        </div>
        <div className="small faint" style={{ marginTop: 8 }}>
          退出登录只会清除本地保存的令牌，不会断开连接；需要重新登录时再点「连接」即可。
        </div>
      </div>

      <div className="card">
        <div className="row">
          <div style={{ flex: 1 }}>
            <h3 className="card-title">
              连接状态
              {connected ? <Tag kind="ok">已连接</Tag> : <Tag>未连接</Tag>}
              {transportView(status?.transport, connected)}
            </h3>
            <p className="card-desc" style={{ margin: 0 }}>
              连接时会先尝试 UDP（QUIC），失败后自动回退到 TCP；下方的时延可帮助判断当前走的是哪条链路。
            </p>
          </div>
          <button className="btn btn-sm" onClick={() => void loadStatus()}>
            刷新
          </button>
        </div>

        <div className="grid-4" style={{ marginTop: 12 }}>
          <div className="stat">
            <div className="label">服务器地址</div>
            <div className="value mono" style={{ fontSize: 15 }}>
              {status?.address || `${host}:${port}`}
            </div>
            <div className="sub">{status?.since ? `连接于 ${formatTime(status.since)}` : '—'}</div>
          </div>
          <div className="stat">
            <div className="label">UDP 时延</div>
            <div className="value">{status?.udpMs ? `${status.udpMs} ms` : '—'}</div>
            <div className="sub truncate" title={status?.udpError || ''}>
              {status?.udpError ? 'UDP 探测失败' : 'QUIC 通道'}
            </div>
          </div>
          <div className="stat">
            <div className="label">TCP 时延</div>
            <div className="value">{status?.tcpMs ? `${status.tcpMs} ms` : '—'}</div>
            <div className="sub truncate" title={status?.tcpError || ''}>
              {status?.tcpError ? 'TCP 不可用' : '回退通道'}
            </div>
          </div>
          <div className="stat">
            <div className="label">证书指纹</div>
            <div className="value mono truncate" style={{ fontSize: 12 }} title={status?.fingerprint || ''}>
              {status?.fingerprint || '—'}
            </div>
            <div className="sub">用于校验服务器身份</div>
          </div>
        </div>

        {status?.lastError ? (
          <div className="small" style={{ color: 'var(--danger)', marginTop: 10 }}>
            最近错误：{status.lastError}
          </div>
        ) : null}

        {user ? (
          <>
            <div className="divider" />
            <div className="row row-wrap">
              <Tag kind="ok">已登录</Tag>
              <span className="mono">{user.displayName || user.username}</span>
              <span className="small faint">@{user.username}</span>
              <Tag kind="info">{user.role || 'user'}</Tag>
              {user.email ? <span className="small faint">{user.email}</span> : null}
              {user.createdAt ? <span className="small faint">注册于 {formatTime(user.createdAt)}</span> : null}
            </div>
            <div className="col" style={{ gap: 6, marginTop: 10 }}>
              <div className="row small faint" style={{ justifyContent: 'space-between' }}>
                <span>
                  配额使用：{formatBytes(user.usedBytes)} / {formatBytes(user.quotaBytes)}
                </span>
                <span>{percent(user.usedBytes, user.quotaBytes).toFixed(1)}%</span>
              </div>
              <Progress percent={percent(user.usedBytes, user.quotaBytes)} />
            </div>
          </>
        ) : (
          <div className="small faint" style={{ marginTop: 10 }}>
            当前未登录服务器账号，登录后即可使用云端存储与笔记同步。
          </div>
        )}
      </div>

      <div className="card">
        <h3 className="card-title">账号</h3>
        <p className="card-desc">账号用于同步笔记、使用云端存储以及中转隧道认证。</p>

        {user ? (
          <div className="col">
            <div className="row row-wrap">
              <Tag kind="ok">已登录</Tag>
              <span className="mono">{user.displayName || user.username}</span>
              <span className="small faint">@{user.username}</span>
              <div className="spacer" />
              <button className="btn btn-sm" onClick={() => void logout()} disabled={loggingOut}>
                退出登录
              </button>
            </div>
            <div className="divider" />
            <div className="grid-3">
              <Field label="当前密码">
                <input
                  className="input"
                  type="password"
                  value={pwdOld}
                  onChange={(e) => setPwdOld(e.target.value)}
                  placeholder="••••••••"
                />
              </Field>
              <Field label="新密码" hint="至少 8 位">
                <input
                  className="input"
                  type="password"
                  value={pwdNew}
                  onChange={(e) => setPwdNew(e.target.value)}
                  placeholder="至少 8 位"
                />
              </Field>
              <Field label="确认新密码">
                <input
                  className="input"
                  type="password"
                  value={pwdNew2}
                  onChange={(e) => setPwdNew2(e.target.value)}
                  placeholder="再输入一次"
                />
              </Field>
            </div>
            <div className="row">
              <div className="spacer" />
              <button className="btn btn-primary btn-sm" onClick={() => void doChangePassword()} disabled={pwdBusy}>
                {pwdBusy ? '提交中…' : '修改密码'}
              </button>
            </div>
          </div>
        ) : (
          <>
            <Tabs<AuthTab>
              tabs={[
                { id: 'login', label: '登录' },
                { id: 'register', label: '注册' },
              ]}
              value={authTab}
              onChange={setAuthTab}
            />
            {authTab === 'login' ? (
              <div className="grid-2">
                <Field label="用户名">
                  <input className="input" value={loginUser} onChange={(e) => setLoginUser(e.target.value)} />
                </Field>
                <Field label="密码" hint="至少 8 位">
                  <input
                    className="input"
                    type="password"
                    value={loginPass}
                    onChange={(e) => setLoginPass(e.target.value)}
                    onKeyDown={(e) => e.key === 'Enter' && void doLogin()}
                  />
                </Field>
                <div className="row" style={{ gridColumn: '1 / -1' }}>
                  <div className="spacer" />
                  <button className="btn btn-primary" onClick={() => void doLogin()} disabled={loginBusy}>
                    {loginBusy ? '登录中…' : '登录'}
                  </button>
                </div>
              </div>
            ) : (
              <div className="grid-2">
                <Field label="用户名">
                  <input className="input" value={regUser} onChange={(e) => setRegUser(e.target.value)} />
                </Field>
                <Field label="显示名称（可选）">
                  <input className="input" value={regDisplay} onChange={(e) => setRegDisplay(e.target.value)} />
                </Field>
                <Field label="密码" hint="至少 8 位">
                  <input className="input" type="password" value={regPass} onChange={(e) => setRegPass(e.target.value)} />
                </Field>
                <Field label="确认密码">
                  <input className="input" type="password" value={regPass2} onChange={(e) => setRegPass2(e.target.value)} />
                </Field>
                <Field label="邮箱（可选）">
                  <input className="input" value={regEmail} onChange={(e) => setRegEmail(e.target.value)} />
                </Field>
                <div className="row" style={{ alignItems: 'flex-end' }}>
                  <div className="spacer" />
                  <button className="btn btn-primary" onClick={() => void doRegister()} disabled={regBusy}>
                    {regBusy ? '注册中…' : '注册并登录'}
                  </button>
                </div>
              </div>
            )}
          </>
        )}
      </div>

      <div className="card">
        <div className="row">
          <div style={{ flex: 1 }}>
            <h3 className="card-title">云端存储</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              服务器为每个账号分配独立存储空间，可在此上传、下载与整理文件。
              {connected ? '' : ' 未连接服务器时不可用。'}
            </p>
          </div>
          <button className="btn btn-sm" onClick={refreshCloud} disabled={!connected || cloudLoading}>
            刷新
          </button>
          <button className="btn btn-sm" onClick={() => setMkdirOpen(true)} disabled={!connected}>
            新建文件夹
          </button>
          <button className="btn btn-sm" onClick={() => void uploadToCloud()} disabled={!connected || uploadBusy}>
            {uploadBusy ? '上传中…' : '上传'}
          </button>
        </div>

        <div className="divider" />

        <div className="grid-2">
          <div className="stat">
            <div className="label">已用空间</div>
            <div className="value">{formatBytes(usage?.usedBytes ?? user?.usedBytes ?? 0)}</div>
            <div className="sub">配额 {formatBytes(usage?.quotaBytes ?? user?.quotaBytes ?? 0)}</div>
          </div>
          <div className="stat">
            <div className="label">使用率</div>
            <div className="value">
              {percent(usage?.usedBytes ?? user?.usedBytes ?? 0, usage?.quotaBytes ?? user?.quotaBytes ?? 0).toFixed(1)}%
            </div>
            <Progress
              percent={percent(usage?.usedBytes ?? user?.usedBytes ?? 0, usage?.quotaBytes ?? user?.quotaBytes ?? 0)}
            />
          </div>
        </div>

        {transfers.length > 0 ? (
          <div className="col" style={{ gap: 6, marginTop: 12 }}>
            {transfers.map((t) => (
              <div className="row" key={t.key} style={{ gap: 10 }}>
                <span className="small faint truncate mono" style={{ maxWidth: 280 }} title={t.key}>
                  正在上传 {t.key.replace('upload:', '')}
                </span>
                <div style={{ flex: 1 }}>
                  <Progress percent={t.total > 0 ? percent(t.done, t.total) : 35} indeterminate={t.total <= 0} />
                </div>
                <span className="small mono faint nowrap">
                  {formatBytes(t.done)}
                  {t.total > 0 ? ` / ${formatBytes(t.total)}` : ''}
                </span>
              </div>
            ))}
          </div>
        ) : null}

        <div className="row" style={{ marginTop: 12 }}>
          <div className="crumbs" style={{ flex: 1 }}>
            <span className="crumb" onClick={() => goCloud('/')}>
              根目录
            </span>
            {crumbs.map((c) => (
              <span key={c.path} className="row" style={{ gap: 2 }}>
                <span className="faint">/</span>
                <span className="crumb" onClick={() => goCloud(c.path)}>
                  {c.label}
                </span>
              </span>
            ))}
          </div>
          <button className="btn btn-sm" onClick={() => void downloadFromCloud()} disabled={!connected || !selectedEntry || selectedEntry.isDir}>
            下载
          </button>
          <button className="btn btn-danger btn-sm" onClick={() => setRemoveTarget(selectedEntry)} disabled={!connected || !selectedEntry}>
            删除
          </button>
        </div>

        {!connected ? (
          <Empty icon="◎" title="尚未连接服务器" hint="先在上方点击「连接」" />
        ) : cloudLoading ? (
          <div style={{ padding: 20 }}>
            <Spinner label="读取云端目录" />
          </div>
        ) : entries.length === 0 ? (
          <Empty icon="▤" title="这里是空的" hint="点击「上传」把本地文件存到云端" />
        ) : (
          <div className="list">
            {entries.map((entry) => (
              <div
                key={entry.path || entry.name}
                className={`file-row ${selectedEntry?.path === entry.path ? 'selected' : ''}`}
                onClick={() => setSelectedEntry(entry)}
                onDoubleClick={() => entry.isDir && goCloud(entry.path)}
                title={entry.isDir ? '双击进入文件夹' : entry.path}
              >
                <span className="file-icon">{entry.isDir ? '📁' : '📄'}</span>
                <span className="file-name">{entry.name}</span>
                <span className="file-size">{entry.isDir ? '文件夹' : formatBytes(entry.size)}</span>
                <span className="file-size" style={{ width: 132, textAlign: 'right' }}>
                  {entry.modTime ? formatTime(entry.modTime) : '—'}
                </span>
                <button
                  className="btn btn-ghost btn-sm"
                  onClick={(e) => {
                    e.stopPropagation()
                    setRemoveTarget(entry)
                  }}
                  title="删除"
                >
                  ✕
                </button>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="card">
        <div className="row">
          <div style={{ flex: 1 }}>
            <h3 className="card-title">
              中转隧道
              {relay ? relay.available ? <Tag kind="ok">可用</Tag> : <Tag kind="warn">不可用</Tag> : null}
            </h3>
            <p className="card-desc" style={{ margin: 0 }}>
              在 SSH 主机中把路由设置为「中转（relay）」时，连接会经由这台同步服务器转发，适合本机无法直连目标主机的场景。
            </p>
          </div>
          <button className="btn btn-sm" onClick={() => void loadRelay()}>
            检测
          </button>
        </div>
        {relay ? (
          <div className="small faint mono" style={{ marginTop: 10 }}>
            {relay.detail || (relay.available ? '服务端已开启中转能力' : '服务端未开启中转能力')}
          </div>
        ) : (
          <div className="small faint" style={{ marginTop: 10 }}>
            点击「检测」查看服务端是否支持中转。
          </div>
        )}
      </div>

      {mkdirOpen ? (
        <Modal
          title="新建文件夹"
          onClose={() => !mkdirBusy && setMkdirOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setMkdirOpen(false)} disabled={mkdirBusy}>
                取消
              </button>
              <button className="btn btn-primary" onClick={() => void doMkdir()} disabled={mkdirBusy}>
                创建
              </button>
            </>
          }
        >
          <Field label="文件夹名称" hint={`创建位置：${cloudPath}`}>
            <input
              className="input"
              value={mkdirName}
              onChange={(e) => setMkdirName(e.target.value)}
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && void doMkdir()}
            />
          </Field>
        </Modal>
      ) : null}

      {removeTarget ? (
        <Confirm
          title="删除云端文件"
          danger
          message={`确定要删除「${removeTarget.name}」吗？${removeTarget.isDir ? '目录及其内容都会被删除。' : ''}此操作不可恢复。`}
          onCancel={() => setRemoveTarget(null)}
          onConfirm={() => void removeEntry()}
        />
      ) : null}
    </div>
  )
}


