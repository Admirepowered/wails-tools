import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, onEvent, type AdminDevice, type AdminUser, type DeviceSummary, type RemoteEntry, type SecretSyncStatus, type ServerStatus, type Settings } from '../lib/api'
import { Confirm, Empty, Field, Modal, Progress, Spinner, Switch, Tabs, Tag, useToast } from '../components/ui'
import { formatBytes, formatTime, percent, relativeTime } from '../lib/format'
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

  // devices + admin
  const [myDevices, setMyDevices] = useState<DeviceSummary | null>(null)
  const [allowRegister, setAllowRegister] = useState(true)
  const [maxDevices, setMaxDevices] = useState(5)
  const [adminUsers, setAdminUsers] = useState<AdminUser[]>([])
  const [adminBusy, setAdminBusy] = useState(false)
  const [newUserName, setNewUserName] = useState('')
  const [newUserPass, setNewUserPass] = useState('')
  const [newUserRole, setNewUserRole] = useState<'user' | 'admin'>('user')
  const [deviceUser, setDeviceUser] = useState<AdminUser | null>(null)
  const [devices, setDevices] = useState<AdminDevice[]>([])
  const [delUser, setDelUser] = useState<AdminUser | null>(null)

  // encrypted secret sync
  const [secretSync, setSecretSync] = useState<SecretSyncStatus | null>(null)
  const [secretBusy, setSecretBusy] = useState(false)
  const [unlockPass, setUnlockPass] = useState('')
  const [pwdConfirm, setPwdConfirm] = useState(false)

  const synced = useRef(false)
  const status = props.serverStatus ?? localStatus
  const connected = status?.connected === true
  const authed = !!status?.user
  const user = status?.user
  const isAdmin = user?.role === 'admin'

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

  const loadMyDevices = useCallback(async () => {
    try {
      setMyDevices(await api.MyDevices())
    } catch {
      setMyDevices(null)
    }
  }, [])

  const loadSecretSync = useCallback(async () => {
    try {
      setSecretSync(await api.SecretSyncStatus())
    } catch {
      setSecretSync(null)
    }
  }, [])

  const loadAdmin = useCallback(async () => {
    if (!isAdmin) return
    try {
      const info = await api.AdminSettingsInfo()
      setAllowRegister(!!info.allowRegister)
      setMaxDevices(Number(info.maxDevices) || 5)
      setAdminUsers(await api.AdminUsers())
    } catch (err) {
      toast.error(err)
    }
  }, [isAdmin, toast])

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

  useEffect(() => {
    if (!connected || !authed) {
      setMyDevices(null)
      setAdminUsers([])
      return
    }
    void loadMyDevices()
    void loadAdmin()
    void loadSecretSync()
  }, [connected, authed, isAdmin, loadMyDevices, loadAdmin, loadSecretSync])

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
      const cur = await api.GetSettings()
      await api.SaveSettings({ ...cur, server: { ...cur.server, autoConnect: value } })
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
    // Confirm before touching anything: rotating the password destroys every
    // secret that was encrypted with the old one.
    setPwdConfirm(true)
  }

  const applyChangePassword = async () => {
    setPwdConfirm(false)
    setPwdBusy(true)
    try {
      const res = await api.ChangeServerPassword(pwdOld, pwdNew)
      setPwdOld('')
      setPwdNew('')
      setPwdNew2('')
      const cleared = typeof res?.vaultCleared === 'number' ? Number(res.vaultCleared) : 0
      toast.push(cleared > 0 ? `密码已修改；服务端已删除 ${cleared} 个旧密钥包` : '密码已修改')
      await loadSecretSync()
      void props.reloadSettings()
    } catch (err) {
      toast.error(err)
    } finally {
      setPwdBusy(false)
    }
  }

  /* ---------------- encrypted secret sync ---------------- */

  const syncSecrets = async () => {
    setSecretBusy(true)
    try {
      const res = await api.SyncSecretsNow()
      const applied = res?.applied ?? {}
      const parts: string[] = []
      if (applied.hosts) parts.push(`主机 +${applied.hosts}`)
      if (applied.keys) parts.push(`密钥 +${applied.keys}`)
      if (applied.ftp) parts.push(`FTP +${applied.ftp}`)
      toast.push(parts.length ? `密钥已同步（${parts.join('，')}）` : '密钥已同步')
      await loadSecretSync()
    } catch (err) {
      toast.error(err)
    } finally {
      setSecretBusy(false)
    }
  }

  const toggleSecretSync = async (enabled: boolean) => {
    try {
      setSecretSync(await api.SetSecretSyncEnabled(enabled))
      toast.push(enabled ? '已开启密钥自动同步' : '已关闭密钥自动同步')
    } catch (err) {
      toast.error(err)
    }
  }

  const forgetSavedPassword = async () => {
    try {
      setSecretSync(await api.ForgetSavedPassword())
      toast.push('已删除本机保存的密码')
    } catch (err) {
      toast.error(err)
    }
  }

  const unlockSecretSync = async () => {
    if (!unlockPass) {
      toast.push('请输入账号密码', 'warn')
      return
    }
    setSecretBusy(true)
    try {
      await api.UnlockSecretSync(unlockPass)
      setUnlockPass('')
      toast.push('加密同步已启用')
      await loadSecretSync()
    } catch (err) {
      toast.error(err)
    } finally {
      setSecretBusy(false)
    }
  }

  const syncEverything = async () => {
    setSecretBusy(true)
    try {
      const res = await api.SyncAllNow()
      const errs = Array.isArray(res?.errors) ? res.errors : []
      if (errs.length > 0) {
        toast.push(`同步完成（${errs.join('；')}）`, 'warn')
      } else {
        const notes = res?.notes ?? {}
        const pulled = typeof notes.pulled === 'number' ? notes.pulled : 0
        const shots = res?.screenshots ?? {}
        const pulledShots = typeof shots.pulled === 'number' ? shots.pulled : 0
        toast.push(`全部同步完成：笔记拉取 ${pulled} 条，截图 ${pulledShots} 张`)
      }
      await loadSecretSync()
    } catch (err) {
      toast.error(err)
    } finally {
      setSecretBusy(false)
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

  /* ---------------- devices & admin ---------------- */

  const toggleRegister = async (next: boolean) => {
    setAllowRegister(next)
    try {
      const res = await api.AdminSetRegistration(next)
      setAllowRegister(!!res.allowRegister)
      toast.push(res.allowRegister ? '已开启公开注册' : '已关闭公开注册')
    } catch (err) {
      setAllowRegister(!next)
      toast.error(err)
    }
  }

  const createUser = async () => {
    const name = newUserName.trim()
    if (!name) {
      toast.push('请输入用户名', 'warn')
      return
    }
    if (!checkPasswordLength(newUserPass)) return
    setAdminBusy(true)
    try {
      await api.AdminCreateUser(name, newUserPass, newUserRole)
      setNewUserName('')
      setNewUserPass('')
      setNewUserRole('user')
      toast.push('用户已创建')
      await loadAdmin()
    } catch (err) {
      toast.error(err)
    } finally {
      setAdminBusy(false)
    }
  }

  const removeUser = async () => {
    const target = delUser
    if (!target) return
    setDelUser(null)
    try {
      await api.AdminDeleteUser(target.id)
      toast.push(`已删除 ${target.username}`)
      await loadAdmin()
    } catch (err) {
      toast.error(err)
    }
  }

  const setRole = async (target: AdminUser, role: 'admin' | 'user') => {
    try {
      await api.AdminSetUserRole(target.id, role)
      toast.push(role === 'admin' ? `${target.username} 已设为管理员` : `${target.username} 已设为普通用户`)
      await loadAdmin()
    } catch (err) {
      toast.error(err)
    }
  }

  const openDevices = async (target: AdminUser) => {
    setDeviceUser(target)
    setDevices([])
    try {
      setDevices(await api.AdminDevices(target.id))
    } catch (err) {
      toast.error(err)
    }
  }

  const removeAdminDevice = async (deviceId: string) => {
    const target = deviceUser
    if (!target) return
    try {
      await api.AdminRemoveDevice(target.id, deviceId)
      setDevices(await api.AdminDevices(target.id))
      await loadAdmin()
      toast.push('设备已移除')
    } catch (err) {
      toast.error(err)
    }
  }

  const removeMyDevice = async (deviceId: string) => {
    try {
      await api.RemoveMyDevice(deviceId)
      await loadMyDevices()
      toast.push('设备已移除')
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
            <div className="small faint" style={{ marginTop: 8 }}>
              注意：密码是密钥包的唯一钥匙。修改密码后，已同步的 SSH 私钥与保存的主机/FTP 密码将无法解密，会被自动清除（主机名、地址、公钥等列表信息保留）。
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

      {authed ? (
        <div className="card">
          <div className="row">
            <div style={{ flex: 1 }}>
              <h3 className="card-title">
                我的设备
                {myDevices ? (
                  <Tag kind={myDevices.devices.length >= myDevices.maxDevices ? 'warn' : 'info'}>
                    {myDevices.devices.length}/{myDevices.maxDevices}
                  </Tag>
                ) : null}
              </h3>
              <p className="card-desc" style={{ margin: 0 }}>
                同一账号最多可登录 {myDevices?.maxDevices ?? maxDevices} 台设备。设备满额时先在这里移除旧设备，再用新机器登录。
              </p>
            </div>
          </div>
          {myDevices && myDevices.devices.length > 0 ? (
            <div className="list" style={{ marginTop: 10 }}>
              {myDevices.devices.map((d) => (
                <div key={d.id} className="file-row">
                  <span className="file-icon">🖥</span>
                  <span className="file-name">{d.name || d.id.slice(0, 8)}</span>
                  <span className="file-size">{d.lastSeen ? relativeTime(d.lastSeen) : '—'}</span>
                  <button className="btn btn-ghost btn-sm" title="移除该设备" onClick={() => void removeMyDevice(d.id)}>
                    ✕
                  </button>
                </div>
              ))}
            </div>
          ) : (
            <div className="small faint" style={{ marginTop: 10 }}>
              暂无已登记设备，下次登录时会自动登记本机。
            </div>
          )}
        </div>
      ) : null}

      {authed ? (
        <div className="card">
          <div className="row">
            <div style={{ flex: 1 }}>
              <h3 className="card-title">
                账号数据同步
                {secretSync?.keyReady ? <Tag kind="ok">密钥就绪</Tag> : <Tag kind="warn">等待密码</Tag>}
                {secretSync && !secretSync.enabled ? <Tag>已关闭</Tag> : null}
              </h3>
              <p className="card-desc" style={{ margin: 0 }}>
                笔记、订阅与自定义节点、截图、SSH 主机/私钥、FTP 账号、网盘账号都会在登录后自动同步；密码/私钥/Cookie 这类敏感内容用<b>账号原始密码</b>在本机加密后上传，服务器只保存密文与密码哈希。换机登录同一账号即可还原，无需重新导入。
              </p>
            </div>
            <label className="row" style={{ gap: 8 }}>
              <span className="small faint">自动同步</span>
              <Switch checked={!!secretSync?.enabled} onChange={(v) => void toggleSecretSync(v)} />
            </label>
          </div>

          <div className="row row-wrap" style={{ marginTop: 12, gap: 16 }}>
            <span className="small faint">SSH 主机 {secretSync?.hosts ?? 0}</span>
            <span className="small faint">SSH 密钥 {secretSync?.keys ?? 0}</span>
            <span className="small faint">FTP 账号 {secretSync?.ftp ?? 0}</span>
            <span className="small faint">网盘账号 {secretSync?.netdisk ?? 0}</span>
            <span className="small faint">最近同步 {secretSync?.lastSync ? formatTime(secretSync.lastSync) : '—'}</span>
            {secretSync?.hasPassword ? <span className="small faint">密码已存本机</span> : <span className="small faint">本机未保存密码</span>}
          </div>

          {secretSync?.lastError ? (
            <div className="small" style={{ color: 'var(--danger)', marginTop: 8 }}>
              {secretSync.lastError}
            </div>
          ) : null}
          {secretSync && !secretSync.keyReady ? (
            <div className="row" style={{ marginTop: 10, gap: 8, flexWrap: 'wrap' }}>
              <input
                className="input"
                type="password"
                placeholder="输入账号密码以启用加密同步"
                value={unlockPass}
                onChange={(e) => setUnlockPass(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && void unlockSecretSync()}
                style={{ maxWidth: 260 }}
              />
              <button className="btn btn-sm" disabled={secretBusy} onClick={() => void unlockSecretSync()}>
                {secretBusy ? '启用中…' : '启用同步'}
              </button>
              <span className="small faint">密码只保存在本机（用机器密钥加密），服务器仍只有哈希。</span>
            </div>
          ) : null}

          <div className="row" style={{ marginTop: 12, gap: 8 }}>
            <button className="btn btn-sm" disabled={secretBusy || !secretSync?.keyReady} onClick={() => void syncSecrets()}>
              {secretBusy ? '同步中…' : '立即同步'}
            </button>
            <button className="btn btn-ghost btn-sm" disabled={secretBusy} onClick={() => void syncEverything()}>
              全部同步（笔记/订阅/网盘/截图）
            </button>
            <button
              className="btn btn-ghost btn-sm"
              disabled={!secretSync?.hasPassword}
              title="删除本机保存的密码与内存中的密钥（云端密文保留）"
              onClick={() => void forgetSavedPassword()}
            >
              忘记本机密码
            </button>
          </div>
        </div>
      ) : null}

      {isAdmin ? (
        <div className="card">
          <div className="row">
            <div style={{ flex: 1 }}>
              <h3 className="card-title">
                服务器管理
                <Tag kind="info">管理员</Tag>
              </h3>
              <p className="card-desc" style={{ margin: 0 }}>
                管理注册开关、账号权限与设备绑定。第一个注册的账号默认是管理员。
              </p>
            </div>
            <div className="row" style={{ gap: 8 }}>
              <span className="small faint">公开注册</span>
              <Switch checked={allowRegister} onChange={(v) => void toggleRegister(v)} />
            </div>
          </div>

          <div className="list" style={{ marginTop: 12 }}>
            {adminUsers.length === 0 ? (
              <div className="small faint">暂无用户</div>
            ) : (
              adminUsers.map((u) => {
                const self = u.username === user?.username
                return (
                  <div key={u.id} className="file-row">
                    <span className="file-icon">{u.role === 'admin' ? '👑' : '👤'}</span>
                    <span className="file-name">
                      {u.username}
                      {self ? '（我）' : ''}
                    </span>
                    <span className="file-size" style={{ width: 80 }}>
                      <Tag kind={u.role === 'admin' ? 'info' : 'default'}>{u.role === 'admin' ? '管理员' : '用户'}</Tag>
                    </span>
                    <span className="file-size" style={{ width: 84, textAlign: 'right' }}>
                      设备 {u.devices} 台
                    </span>
                    <button className="btn btn-ghost btn-sm" onClick={() => void openDevices(u)}>
                      设备
                    </button>
                    <button
                      className="btn btn-ghost btn-sm"
                      disabled={self}
                      onClick={() => void setRole(u, u.role === 'admin' ? 'user' : 'admin')}
                    >
                      {u.role === 'admin' ? '降为普通' : '设为管理员'}
                    </button>
                    <button className="btn btn-danger btn-sm" disabled={self} onClick={() => setDelUser(u)}>
                      删除
                    </button>
                  </div>
                )
              })
            )}
          </div>

          <div className="row" style={{ marginTop: 14, gap: 8, flexWrap: 'wrap' }}>
            <input
              className="input"
              placeholder="用户名"
              value={newUserName}
              onChange={(e) => setNewUserName(e.target.value)}
              style={{ maxWidth: 180 }}
            />
            <input
              className="input"
              type="password"
              placeholder="密码"
              value={newUserPass}
              onChange={(e) => setNewUserPass(e.target.value)}
              style={{ maxWidth: 180 }}
            />
            <Tabs
              tabs={[
                { id: 'user', label: '普通用户' },
                { id: 'admin', label: '管理员' },
              ]}
              value={newUserRole}
              onChange={setNewUserRole}
            />
            <button className="btn btn-primary btn-sm" onClick={() => void createUser()} disabled={adminBusy}>
              新增用户
            </button>
          </div>
        </div>
      ) : null}

      {deviceUser ? (
        <Modal
          title={`${deviceUser.username} 的设备`}
          onClose={() => {
            setDeviceUser(null)
            setDevices([])
          }}
          footer={
            <button
              className="btn"
              onClick={() => {
                setDeviceUser(null)
                setDevices([])
              }}
            >
              关闭
            </button>
          }
        >
          {devices.length === 0 ? (
            <Empty icon="🖥" title="没有登记设备" hint="该账号还没有登录过任何设备" />
          ) : (
            <div className="list">
              {devices.map((d) => (
                <div key={d.id} className="file-row">
                  <span className="file-icon">🖥</span>
                  <span className="file-name">{d.name || d.id.slice(0, 8)}</span>
                  <span className="file-size">{d.lastSeen ? relativeTime(d.lastSeen) : '—'}</span>
                  <button className="btn btn-ghost btn-sm" title="移除该设备" onClick={() => void removeAdminDevice(d.id)}>
                    ✕
                  </button>
                </div>
              ))}
            </div>
          )}
        </Modal>
      ) : null}

      {delUser ? (
        <Confirm
          title="删除账号"
          danger
          message={`确定删除账号「${delUser.username}」吗？该账号的令牌会立即失效，此操作不可恢复。`}
          onCancel={() => setDelUser(null)}
          onConfirm={() => void removeUser()}
        />
      ) : null}

      {pwdConfirm ? (
        <Confirm
          title="修改密码"
          danger
          message="修改密码会立即删除服务器上所有用它加密的密钥包，并清空本机保存的 SSH 私钥、主机密码与 FTP 密码。这些内容无法恢复，需要重新生成或导入。确定继续？"
          onCancel={() => setPwdConfirm(false)}
          onConfirm={() => void applyChangePassword()}
        />
      ) : null}

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




