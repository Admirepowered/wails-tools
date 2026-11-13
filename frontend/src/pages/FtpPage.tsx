import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, onEvent, type FTPEntry, type FTPProfile, type FTPSession } from '../lib/api'
import { Confirm, Empty, Field, Modal, Progress, Spinner, Switch, Tag, useToast } from '../components/ui'
import { basename, formatBytes, formatSpeed, formatTime, joinPath, relativeTime } from '../lib/format'
import type { PageProps } from '../App'

/* ---------------- helpers ---------------- */

function protocolLabel(profile: FTPProfile): string {
  if (profile.config?.tls) return 'FTPS 隐式'
  if (profile.config?.explicitFtps) return 'FTPS 显式'
  return 'FTP'
}

function protocolKind(profile: FTPProfile): 'default' | 'ok' {
  return profile.config?.tls || profile.config?.explicitFtps ? 'ok' : 'default'
}

function parentOf(p: string): string {
  const trimmed = p.replace(/\/+$/, '')
  if (!trimmed) return '/'
  const i = trimmed.lastIndexOf('/')
  if (i < 0) return '/'
  return i === 0 ? '/' : trimmed.slice(0, i)
}

function crumbParts(p: string): Array<{ label: string; path: string }> {
  const parts = p.split('/').filter(Boolean)
  const out: Array<{ label: string; path: string }> = []
  let acc = ''
  for (const part of parts) {
    acc = `${acc}/${part}`
    out.push({ label: part, path: acc })
  }
  return out
}

interface PromptState {
  title: string
  label: string
  value: string
  resolve: (value: string | null) => void
}

interface XferState {
  key: string
  label: string
  done: number
  total: number
  percent: number
  speedBps: number
  path: string
  finished: boolean
}

interface TransferProgress {
  key: string
  kind: string
  progress?: { done?: number; total?: number; percent?: number; speedBps?: number; path?: string }
}

interface TransferDone {
  key: string
  kind: string
  path?: string
  bytes?: number
}

/* ---------------- page ---------------- */

export default function FtpPage({}: PageProps) {
  const toast = useToast()
  const [profiles, setProfiles] = useState<FTPProfile[]>([])
  const [sessions, setSessions] = useState<FTPSession[]>([])
  const [loading, setLoading] = useState(true)
  const [activeId, setActiveId] = useState<string | null>(null)
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<FTPProfile | null>(null)
  const [pendingDelete, setPendingDelete] = useState<FTPProfile | null>(null)
  const [connectingId, setConnectingId] = useState<string | null>(null)

  const loadProfiles = useCallback(async () => {
    try {
      setProfiles(await api.ListFTPProfiles())
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const loadSessions = useCallback(async () => {
    try {
      const list = await api.FTPSessions()
      setSessions(list)
      setActiveId((cur) => (cur && list.some((s) => s.id === cur) ? cur : list.length > 0 ? list[0].id : null))
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const refreshAll = useCallback(async () => {
    await Promise.all([loadProfiles(), loadSessions()])
  }, [loadProfiles, loadSessions])

  useEffect(() => {
    void (async () => {
      await refreshAll()
      setLoading(false)
    })()
  }, [refreshAll])

  useEffect(() => {
    const off = onEvent<FTPSession[]>('ftp:sessions', () => {
      void loadSessions()
    })
    return () => off()
  }, [loadSessions])

  const activeSession = useMemo(() => sessions.find((s) => s.id === activeId) ?? null, [sessions, activeId])

  const openEditor = (profile: FTPProfile | null) => {
    setEditing(profile)
    setEditorOpen(true)
  }

  const connect = async (profile: FTPProfile) => {
    if (!profile.id) return
    setConnectingId(profile.id)
    try {
      const session = await api.ConnectFTP(profile.id)
      setSessions((prev) => [...prev.filter((s) => s.id !== session.id), session])
      setActiveId(session.id)
      toast.push(`已连接 ${session.name || profile.name}`)
    } catch (err) {
      toast.error(err)
    } finally {
      setConnectingId(null)
    }
  }

  const disconnect = async (id: string) => {
    try {
      await api.DisconnectFTP(id)
      setSessions((prev) => prev.filter((s) => s.id !== id))
      if (activeId === id) setActiveId(null)
      toast.push('已断开连接')
    } catch (err) {
      toast.error(err)
    }
  }

  const removeProfile = async (profile: FTPProfile) => {
    if (!profile.id) return
    try {
      await api.DeleteFTPProfile(profile.id)
      toast.push(`已删除 ${profile.name}`)
      await refreshAll()
    } catch (err) {
      toast.error(err)
    }
  }

  return (
    <div className="split split-wide" style={{ height: 'calc(100vh - 122px)' }}>
      <div className="pane col" style={{ gap: 8, paddingRight: 4 }}>
        <div className="row" style={{ gap: 6 }}>
          <button className="btn btn-primary btn-sm" style={{ flex: 1 }} onClick={() => openEditor(null)}>
            新建连接
          </button>
          <button className="btn btn-sm" onClick={() => void refreshAll()}>
            刷新
          </button>
        </div>
        <div className="row small faint">
          <span>{profiles.length} 个已保存连接</span>
          <div className="spacer" />
          <span>{sessions.length} 个会话</span>
        </div>

        {loading ? (
          <Spinner label="加载 FTP 连接" />
        ) : profiles.length === 0 ? (
          <Empty icon="⇄" title="还没有 FTP 连接" hint="点击“新建连接”添加 FTP / FTPS 服务器" />
        ) : (
          <div className="list" style={{ gap: 6 }}>
            {profiles.map((profile) => (
              <div
                key={profile.id ?? profile.name}
                className="list-row"
                style={{ flexDirection: 'column', alignItems: 'stretch', gap: 6, padding: '8px 10px' }}
              >
                <div className="row" style={{ gap: 6 }}>
                  <span className="name truncate" style={{ flex: 1 }} title={profile.name}>
                    {profile.name || profile.config?.host}
                  </span>
                  <Tag kind={protocolKind(profile)}>{protocolLabel(profile)}</Tag>
                </div>
                <div className="small faint mono truncate">
                  {profile.config?.user || 'anonymous'}@{profile.config?.host}:{profile.config?.port}
                </div>
                <div className="row" style={{ gap: 6 }}>
                  <span className="small faint">{relativeTime(profile.updatedAt)}</span>
                  <div className="spacer" />
                  <button className="btn btn-ghost btn-sm" title="编辑" onClick={() => openEditor(profile)}>
                    ✎
                  </button>
                  <button className="btn btn-ghost btn-sm" title="删除" onClick={() => setPendingDelete(profile)}>
                    ✕
                  </button>
                  <button
                    className="btn btn-primary btn-sm"
                    disabled={connectingId === profile.id}
                    onClick={() => void connect(profile)}
                  >
                    {connectingId === profile.id ? '连接中…' : '连接'}
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="pane" style={{ display: 'flex', flexDirection: 'column', gap: 10, paddingRight: 2 }}>
        <div className="card" style={{ padding: '10px 14px' }}>
          <div className="row" style={{ gap: 8 }}>
            <span className="small faint nowrap">会话</span>
            <select
              className="select"
              style={{ maxWidth: 340 }}
              value={activeId ?? ''}
              onChange={(e) => setActiveId(e.target.value || null)}
            >
              <option value="">未选择</option>
              {sessions.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name} · {s.config?.host}:{s.config?.port}
                </option>
              ))}
            </select>
            {activeSession ? (
              <>
                <Tag kind={activeSession.config?.tls || activeSession.config?.explicitFtps ? 'ok' : 'info'}>
                  {activeSession.config?.tls ? 'FTPS 隐式' : activeSession.config?.explicitFtps ? 'FTPS 显式' : 'FTP'}
                </Tag>
                {activeSession.config?.passive ? <Tag>被动模式</Tag> : <Tag kind="warn">主动模式</Tag>}
              </>
            ) : null}
            <div className="spacer" />
            <button className="btn btn-sm" disabled={!activeSession} onClick={() => activeSession && void disconnect(activeSession.id)}>
              断开
            </button>
          </div>
        </div>

        {activeSession ? (
          <FtpBrowser key={activeSession.id} sessionId={activeSession.id} />
        ) : (
          <div className="card">
            <Empty
              icon="⇄"
              title="未连接任何 FTP 服务器"
              hint={profiles.length === 0 ? '先在左侧新建一个连接' : '在左侧选择一个连接并点击“连接”'}
            />
          </div>
        )}
      </div>

      {editorOpen ? (
        <ProfileEditorModal
          key={editing?.id ?? 'new-profile'}
          initial={editing}
          onClose={() => {
            setEditorOpen(false)
            setEditing(null)
          }}
          onSaved={async (saved) => {
            setEditorOpen(false)
            setEditing(null)
            await refreshAll()
            toast.push(`已保存 ${saved.name}`)
          }}
        />
      ) : null}

      {pendingDelete ? (
        <Confirm
          title="删除连接"
          danger
          message={`确认删除“${pendingDelete.name}”吗？保存的密码会一并移除。`}
          onCancel={() => setPendingDelete(null)}
          onConfirm={() => {
            const profile = pendingDelete
            setPendingDelete(null)
            void removeProfile(profile)
          }}
        />
      ) : null}
    </div>
  )
}

/* ---------------- profile editor ---------------- */

function ProfileEditorModal({
  initial,
  onClose,
  onSaved,
}: {
  initial: FTPProfile | null
  onClose: () => void
  onSaved: (profile: FTPProfile) => Promise<void> | void
}) {
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const [draft, setDraft] = useState(() => ({
    name: initial?.name ?? '',
    host: initial?.config?.host ?? '',
    port: initial?.config?.port ?? 21,
    user: initial?.config?.user ?? '',
    password: '',
    tls: initial?.config?.tls ?? false,
    explicitFtps: initial?.config?.explicitFtps ?? false,
    passive: initial?.config?.passive ?? true,
    timeoutSec: initial?.config?.timeoutSec ?? 30,
  }))

  const patch = (next: Partial<typeof draft>) => setDraft((cur) => ({ ...cur, ...next }))

  const toggleTls = (value: boolean) => {
    setDraft((cur) => ({
      ...cur,
      tls: value,
      explicitFtps: value ? false : cur.explicitFtps,
      port: cur.port === 21 || cur.port === 990 ? (value ? 990 : 21) : cur.port,
    }))
  }

  const save = async () => {
    if (!draft.host.trim()) {
      toast.push('请填写服务器地址', 'warn')
      return
    }
    if (!draft.name.trim()) {
      toast.push('请填写连接名称', 'warn')
      return
    }
    setBusy(true)
    try {
      const password = draft.password
      const payload: FTPProfile = {
        id: initial?.id,
        name: draft.name.trim(),
        config: {
          id: initial?.config?.id ?? initial?.id,
          name: draft.name.trim(),
          host: draft.host.trim(),
          port: Number(draft.port) || 21,
          user: draft.user.trim(),
          password,
          tls: draft.tls,
          explicitFtps: draft.explicitFtps,
          passive: draft.passive,
          timeoutSec: Number(draft.timeoutSec) || 30,
          path: initial?.config?.path,
        },
        password,
        createdAt: initial?.createdAt,
        updatedAt: initial?.updatedAt,
      }
      const saved = await api.SaveFTPProfile(payload)
      await onSaved(saved)
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={initial ? `编辑连接 · ${initial.name}` : '新建 FTP 连接'}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy} onClick={() => void save()}>
            {busy ? '保存中…' : '保存'}
          </button>
        </>
      }
    >
      <Field label="名称">
        <input className="input" value={draft.name} onChange={(e) => patch({ name: e.target.value })} placeholder="例如 图床空间" />
      </Field>

      <div className="grid-2">
        <Field label="服务器地址">
          <input className="input mono" value={draft.host} onChange={(e) => patch({ host: e.target.value })} placeholder="ftp.example.com" />
        </Field>
        <Field label="端口" hint={draft.tls ? '隐式 FTPS 通常为 990' : '显式 FTPS / FTP 通常为 21'}>
          <input
            className="input"
            type="number"
            min={1}
            max={65535}
            value={draft.port}
            onChange={(e) => patch({ port: Number(e.target.value) || 21 })}
          />
        </Field>
      </div>

      <div className="grid-2">
        <Field label="用户名" hint="留空表示匿名登录">
          <input className="input mono" value={draft.user} onChange={(e) => patch({ user: e.target.value })} placeholder="anonymous" />
        </Field>
        <Field label="密码" hint="保存在本机加密存储；留空会清除已保存的密码">
          <input
            className="input"
            type="password"
            value={draft.password}
            onChange={(e) => patch({ password: e.target.value })}
            placeholder="登录密码"
          />
        </Field>
      </div>

      <div className="divider" />

      <div className="row" style={{ justifyContent: 'space-between' }}>
        <div>
          <div className="small">隐式 FTPS</div>
          <div className="small faint">整条连接从建立起就走 TLS（通常 990 端口）</div>
        </div>
        <Switch checked={draft.tls} onChange={toggleTls} title="隐式 FTPS" />
      </div>

      <div className="row" style={{ justifyContent: 'space-between' }}>
        <div>
          <div className="small">显式 FTPS</div>
          <div className="small faint">先明文连接再通过 AUTH TLS 升级加密</div>
        </div>
        <Switch
          checked={draft.explicitFtps}
          onChange={(v) => patch({ explicitFtps: v, tls: v ? false : draft.tls })}
          title="显式 FTPS"
        />
      </div>

      <div className="row" style={{ justifyContent: 'space-between' }}>
        <div>
          <div className="small">被动模式（PASV）</div>
          <div className="small faint">位于 NAT 或防火墙后时建议开启</div>
        </div>
        <Switch checked={draft.passive} onChange={(v) => patch({ passive: v })} title="被动模式" />
      </div>

      <div className="divider" />

      <Field
        label="超时（秒）"
        hint={`连接与读写超时时间 · 当前协议：${
          draft.tls ? '隐式 FTPS' : draft.explicitFtps ? '显式 FTPS' : 'FTP'
        }`}
      >
        <input
          className="input"
          type="number"
          min={5}
          max={600}
          value={draft.timeoutSec}
          onChange={(e) => patch({ timeoutSec: Number(e.target.value) || 30 })}
        />
      </Field>
    </Modal>
  )
}

/* ---------------- remote browser ---------------- */

function FtpBrowser({ sessionId }: { sessionId: string }) {
  const toast = useToast()
  const [dir, setDir] = useState('')
  const [entries, setEntries] = useState<FTPEntry[]>([])
  const [selected, setSelected] = useState<FTPEntry | null>(null)
  const [loading, setLoading] = useState(false)
  const [prompt, setPrompt] = useState<PromptState | null>(null)
  const [pendingRemove, setPendingRemove] = useState<FTPEntry | null>(null)
  const [xfers, setXfers] = useState<Record<string, XferState>>({})
  const dirRef = useRef('')
  const reloadRef = useRef<() => void>(() => {})

  const navigate = useCallback(
    async (target: string) => {
      setLoading(true)
      try {
        const res = await api.FTPList(sessionId, target)
        setEntries(res.entries ?? [])
        setDir(target || res.path || '/')
        setSelected(null)
      } catch (err) {
        toast.error(err)
      } finally {
        setLoading(false)
      }
    },
    [sessionId, toast],
  )

  useEffect(() => {
    dirRef.current = dir
  }, [dir])

  useEffect(() => {
    reloadRef.current = () => {
      void navigate(dirRef.current)
    }
  }, [navigate])

  useEffect(() => {
    void navigate('')
  }, [navigate])

  useEffect(() => {
    const offProgress = onEvent<TransferProgress>('transfer:progress', (ev) => {
      if (!ev?.key || typeof ev.kind !== 'string' || !ev.kind.startsWith('ftp-')) return
      const p = ev.progress
      setXfers((prev) => ({
        ...prev,
        [ev.key]: {
          key: ev.key,
          label: ev.kind === 'ftp-upload' ? '上传' : '下载',
          done: p?.done ?? 0,
          total: p?.total ?? 0,
          percent: p?.percent ?? 0,
          speedBps: p?.speedBps ?? 0,
          path: p?.path ?? '',
          finished: false,
        },
      }))
    })
    const offDone = onEvent<TransferDone>('transfer:done', (ev) => {
      if (!ev?.key || typeof ev.kind !== 'string' || !ev.kind.startsWith('ftp-')) return
      const label = ev.kind === 'ftp-upload' ? '上传' : '下载'
      setXfers((prev) => {
        const cur = prev[ev.key]
        return {
          ...prev,
          [ev.key]: {
            key: ev.key,
            label,
            done: ev.bytes ?? 0,
            total: cur?.total || ev.bytes || 0,
            percent: 100,
            speedBps: 0,
            path: ev.path ?? cur?.path ?? '',
            finished: true,
          },
        }
      })
      toast.push(`${label}完成 · ${basename(ev.path ?? '')} · ${formatBytes(ev.bytes ?? 0)}`)
      if (ev.kind === 'ftp-upload') reloadRef.current()
      window.setTimeout(() => {
        setXfers((prev) => {
          const next = { ...prev }
          delete next[ev.key]
          return next
        })
      }, 4000)
    })
    return () => {
      offProgress()
      offDone()
    }
  }, [toast])

  const ask = (title: string, label: string, initial = '') =>
    new Promise<string | null>((resolve) => setPrompt({ title, label, value: initial, resolve }))

  const closePrompt = (value: string | null) => {
    const current = prompt
    if (current) current.resolve(value && value.trim() ? value.trim() : null)
    setPrompt(null)
  }

  const mkdir = async () => {
    const name = await ask('新建目录', '目录名称')
    if (!name) return
    try {
      await api.FTPMkdir(sessionId, joinPath(dir, name))
      toast.push('目录已创建')
      await navigate(dir)
    } catch (err) {
      toast.error(err)
    }
  }

  const upload = async () => {
    try {
      const local = await api.PickFile('选择要上传的文件')
      if (!local) return
      const remote = joinPath(dir, basename(local))
      const res = await api.FTPUpload(sessionId, local, remote)
      toast.push(`已上传 ${basename(res.path ?? remote)}`)
      await navigate(dir)
    } catch (err) {
      toast.error(err)
    }
  }

  const download = async () => {
    if (!selected) {
      toast.push('请先选择要下载的文件', 'warn')
      return
    }
    if (selected.isDir) {
      toast.push('请选择文件而不是目录', 'warn')
      return
    }
    try {
      const target = await api.PickDirectory('选择保存目录')
      if (!target) return
      const local = joinPath(target, selected.name || basename(selected.path))
      const res = await api.FTPDownload(sessionId, selected.path, local)
      toast.push(`已下载到 ${res.path ?? local}`)
    } catch (err) {
      toast.error(err)
    }
  }

  const rename = async () => {
    if (!selected) {
      toast.push('请先选择要重命名的项目', 'warn')
      return
    }
    const name = await ask('重命名', '新的名称', selected.name)
    if (!name || name === selected.name) return
    try {
      await api.FTPRename(sessionId, selected.path, joinPath(dir, name))
      toast.push('已重命名')
      await navigate(dir)
    } catch (err) {
      toast.error(err)
    }
  }

  const remove = async (entry: FTPEntry) => {
    try {
      await api.FTPRemove(sessionId, entry.path, entry.isDir)
      toast.push('已删除')
      await navigate(dir)
    } catch (err) {
      toast.error(err)
    }
  }

  const crumbs = crumbParts(dir)
  const canGoUp = dir !== '' && dir !== '/'
  const transfers = Object.values(xfers)

  return (
    <div className="col" style={{ gap: 10, minHeight: 0 }}>
      <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
        <button className="btn btn-sm" onClick={() => void navigate(dir)} disabled={loading}>
          刷新
        </button>
        <button className="btn btn-sm" onClick={() => void mkdir()}>
          新建目录
        </button>
        <button className="btn btn-sm" onClick={() => void upload()}>
          上传
        </button>
        <button className="btn btn-sm" onClick={() => void download()} disabled={!selected || selected.isDir}>
          下载
        </button>
        <button className="btn btn-sm" onClick={() => void rename()} disabled={!selected}>
          重命名
        </button>
        <button className="btn btn-danger btn-sm" onClick={() => setPendingRemove(selected)} disabled={!selected}>
          删除
        </button>
        <div className="spacer" />
        {loading ? <Spinner /> : <span className="small faint">{entries.length} 项</span>}
      </div>

      <div className="crumbs">
        <span className="crumb" onClick={() => void navigate('/')}>
          /
        </span>
        {crumbs.map((c) => (
          <span key={c.path} className="row" style={{ gap: 0 }}>
            <span className="faint">›</span>
            <span className="crumb" onClick={() => void navigate(c.path)}>
              {c.label}
            </span>
          </span>
        ))}
      </div>

      {transfers.length > 0 ? (
        <div className="card" style={{ padding: '10px 12px' }}>
          <div className="col" style={{ gap: 8 }}>
            {transfers.map((x) => (
              <div key={x.key} className="row" style={{ gap: 10 }}>
                <Tag kind={x.finished ? 'ok' : 'info'}>{x.label}</Tag>
                <span className="small mono truncate" style={{ flex: 1 }} title={x.path}>
                  {x.path}
                </span>
                <span className="small faint mono nowrap">
                  {formatBytes(x.done)} / {formatBytes(x.total)}
                </span>
                <span className="small faint mono nowrap" style={{ width: 78, textAlign: 'right' }}>
                  {x.finished ? '完成' : formatSpeed(x.speedBps)}
                </span>
                <div style={{ width: 120 }}>
                  <Progress percent={x.finished ? 100 : x.percent} />
                </div>
              </div>
            ))}
          </div>
        </div>
      ) : null}

      <div className="list" style={{ gap: 1 }}>
        {canGoUp ? (
          <div className="file-row" onClick={() => void navigate(parentOf(dir))}>
            <span className="file-icon">↰</span>
            <span className="file-name dim">..</span>
          </div>
        ) : null}
        {entries.length === 0 && !loading ? (
          <Empty icon="📂" title="这个目录是空的" hint="可以上传文件或新建目录" />
        ) : (
          entries.map((entry) => (
            <div
              key={entry.path || entry.name}
              className={`file-row ${selected?.path === entry.path ? 'selected' : ''}`}
              title={entry.path}
              onClick={() => setSelected(entry)}
              onDoubleClick={() => entry.isDir && void navigate(entry.path)}
            >
              <span className="file-icon">{entry.isDir ? '📁' : '📄'}</span>
              <span className="file-name">{entry.name}</span>
              <span className="file-size">{entry.isDir ? '—' : formatBytes(entry.size)}</span>
              <span className="small faint nowrap" style={{ width: 132, textAlign: 'right' }}>
                {formatTime(entry.modTime)}
              </span>
            </div>
          ))
        )}
      </div>

      {prompt ? (
        <Modal
          title={prompt.title}
          onClose={() => closePrompt(null)}
          footer={
            <>
              <button className="btn" onClick={() => closePrompt(null)}>
                取消
              </button>
              <button className="btn btn-primary" onClick={() => closePrompt(prompt.value)}>
                确定
              </button>
            </>
          }
        >
          <Field label={prompt.label}>
            <input
              className="input"
              autoFocus
              value={prompt.value}
              onChange={(e) => setPrompt({ ...prompt, value: e.target.value })}
              onKeyDown={(e) => {
                if (e.key === 'Enter') closePrompt(prompt.value)
              }}
            />
          </Field>
        </Modal>
      ) : null}

      {pendingRemove ? (
        <Confirm
          title="删除远端项目"
          danger
          message={`确认删除${pendingRemove.isDir ? '目录（含其中全部内容）' : '文件'} ${pendingRemove.path} 吗？此操作不可恢复。`}
          onCancel={() => setPendingRemove(null)}
          onConfirm={() => {
            const target = pendingRemove
            setPendingRemove(null)
            void remove(target)
          }}
        />
      ) : null}
    </div>
  )
}
