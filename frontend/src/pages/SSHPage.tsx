import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import {
  api,
  onEvent,
  type DialOptions,
  type RemoteFile,
  type SSHHost,
  type SSHKey,
  type SSHSession,
} from '../lib/api'
import { Confirm, Empty, Field, Modal, Progress, Spinner, Tabs, Tag, useToast } from '../components/ui'
import { basename, formatBytes, formatSpeed, formatTime, joinPath, relativeTime } from '../lib/format'
import type { PageProps } from '../App'

/* ---------------- labels & helpers ---------------- */

const AUTH_LABEL: Record<string, string> = {
  key: '密钥',
  password: '密码',
  'keyboard-interactive': '交互式',
  agent: 'Agent',
  none: '无认证',
}

const ROUTE_LABEL: Record<string, string> = { direct: '直连', jump: '跳板', relay: '中转' }
const ROUTE_KIND: Record<string, 'default' | 'ok' | 'warn' | 'info'> = {
  direct: 'default',
  jump: 'info',
  relay: 'warn',
}

function routeLabel(route?: string): string {
  return ROUTE_LABEL[route ?? ''] ?? route ?? '直连'
}

function routeKind(route?: string): 'default' | 'ok' | 'warn' | 'info' {
  return ROUTE_KIND[route ?? ''] ?? 'default'
}

function authLabel(mode?: string): string {
  return AUTH_LABEL[mode ?? ''] ?? mode ?? '密钥'
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

async function copyText(text: string): Promise<boolean> {
  try {
    const setter = window.runtime?.ClipboardSetText
    if (setter) {
      const ok = await setter(text)
      if (ok) return true
    }
  } catch {
    /* 降级到浏览器剪贴板 */
  }
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

function splitTags(value: string): string[] {
  return value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

/* ---------------- shared types ---------------- */

type TabKey = 'sftp' | 'term' | 'cmd'

const TABS: Array<{ id: TabKey; label: string }> = [
  { id: 'sftp', label: '文件' },
  { id: 'term', label: '终端' },
  { id: 'cmd', label: '命令' },
]

interface HostTest {
  ok: boolean
  transport?: string
  chain?: string[]
  elapsedMs?: number
  uname?: string
  error?: string
}

interface DialState {
  host: SSHHost
  purpose: 'connect' | 'test'
  passphrase: string
  password: string
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

export default function SSHPage({}: PageProps) {
  const toast = useToast()
  const [hosts, setHosts] = useState<SSHHost[]>([])
  const [keys, setKeys] = useState<SSHKey[]>([])
  const [sessions, setSessions] = useState<SSHSession[]>([])
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [activeId, setActiveId] = useState<string | null>(null)
  const [tab, setTab] = useState<TabKey>('sftp')
  const [tests, setTests] = useState<Record<string, HostTest>>({})

  const [hostEditorOpen, setHostEditorOpen] = useState(false)
  const [editingHost, setEditingHost] = useState<SSHHost | null>(null)
  const [pendingHostDelete, setPendingHostDelete] = useState<SSHHost | null>(null)

  const [keysOpen, setKeysOpen] = useState(false)

  const [dial, setDial] = useState<DialState | null>(null)
  const [dialBusy, setDialBusy] = useState(false)

  const loadHosts = useCallback(async () => {
    try {
      setHosts(await api.ListSSHHosts())
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const loadKeys = useCallback(async () => {
    try {
      setKeys(await api.ListSSHKeys())
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const loadSessions = useCallback(async () => {
    try {
      setSessions(await api.SSHSessions())
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const refreshAll = useCallback(async () => {
    await Promise.all([loadHosts(), loadKeys(), loadSessions()])
  }, [loadHosts, loadKeys, loadSessions])

  useEffect(() => {
    void (async () => {
      await refreshAll()
      setLoading(false)
    })()
  }, [refreshAll])

  useEffect(() => {
    const off = onEvent<SSHSession[]>('ssh:sessions', (list) => {
      if (Array.isArray(list)) setSessions(list)
    })
    return () => off()
  }, [])

  const activeSession = useMemo(
    () => sessions.find((s) => s.id === activeId) ?? null,
    [sessions, activeId],
  )
  const sessionId = activeSession?.id ?? ''

  const groups = useMemo(() => {
    const q = search.trim().toLowerCase()
    const filtered = hosts.filter((h) => {
      if (!q) return true
      const haystack = [h.name, h.address, h.user, h.group ?? '', ...(h.tags ?? [])].join(' ').toLowerCase()
      return haystack.includes(q)
    })
    const map = new Map<string, SSHHost[]>()
    for (const host of filtered) {
      const group = (host.group ?? '').trim() || '未分组'
      const list = map.get(group) ?? []
      list.push(host)
      map.set(group, list)
    }
    return [...map.entries()]
  }, [hosts, search])

  const openHostEditor = (host: SSHHost | null) => {
    setEditingHost(host)
    setHostEditorOpen(true)
  }

  const closeHostEditor = () => {
    setHostEditorOpen(false)
    setEditingHost(null)
  }

  const askDial = (host: SSHHost, purpose: 'connect' | 'test') => {
    if (!host.id) {
      toast.push('请先保存该主机', 'warn')
      return
    }
    setDial({ host, purpose, passphrase: '', password: '' })
  }

  const submitDial = async () => {
    if (!dial) return
    const current = dial
    const hostId = current.host.id
    if (!hostId) {
      toast.push('请先保存该主机', 'warn')
      return
    }
    const opts: DialOptions = {
      passphrase: current.passphrase.trim() ? current.passphrase : undefined,
      password: current.password.trim() ? current.password : undefined,
      timeout: 30 * 1e9,
    }
    setDialBusy(true)
    try {
      if (current.purpose === 'test') {
        const res = await api.TestSSHHost(hostId, opts)
        setTests((prev) => ({ ...prev, [hostId]: res }))
        if (res.ok) {
          toast.push(`连接成功 · ${res.transport ?? 'ssh'} · ${res.elapsedMs ?? 0}ms`)
        } else {
          toast.push(res.error || '测试失败', 'error')
        }
      } else {
        const session = await api.ConnectSSH(hostId, opts)
        setSessions((prev) => [...prev.filter((s) => s.id !== session.id), session])
        setActiveId(session.id)
        setTab('sftp')
        toast.push(`已连接 ${session.name || current.host.name}`)
      }
      setDial(null)
    } catch (err) {
      toast.error(err)
    } finally {
      setDialBusy(false)
    }
  }

  const disconnect = async (id: string) => {
    try {
      await api.DisconnectSSH(id)
      setSessions((prev) => prev.filter((s) => s.id !== id))
      if (activeId === id) setActiveId(null)
      toast.push('已断开连接')
    } catch (err) {
      toast.error(err)
    }
  }

  const removeHost = async (host: SSHHost) => {
    if (!host.id) return
    try {
      await api.DeleteSSHHost(host.id)
      toast.push(`已删除 ${host.name}`)
      await refreshAll()
    } catch (err) {
      toast.error(err)
    }
  }

  const renderHost = (host: SSHHost) => {
    const test = host.id ? tests[host.id] : undefined
    return (
      <div className="list-row" key={host.id ?? host.name} style={{ flexDirection: 'column', alignItems: 'stretch', gap: 6, padding: '8px 10px' }}>
        <div className="row" style={{ gap: 6 }}>
          <span className="name truncate" style={{ flex: 1 }} title={host.name}>
            {host.name || host.address}
          </span>
          <Tag kind={routeKind(host.route)}>{routeLabel(host.route)}</Tag>
          <button className="btn btn-ghost btn-sm" title="编辑" onClick={() => openHostEditor(host)}>
            ✎
          </button>
          <button className="btn btn-ghost btn-sm" title="删除" onClick={() => setPendingHostDelete(host)}>
            ✕
          </button>
        </div>
        <div className="small faint mono truncate" title={`${host.user}@${host.address}:${host.port}`}>
          {host.user}@{host.address}:{host.port}
        </div>
        <div className="row" style={{ gap: 6 }}>
          {host.lastTransport ? <Tag kind="info">{host.lastTransport}</Tag> : null}
          {host.lastUsed ? <span className="small faint">{relativeTime(host.lastUsed)}</span> : null}
          <div className="spacer" />
          <button className="btn btn-sm" onClick={() => askDial(host, 'test')}>
            测试
          </button>
          <button className="btn btn-primary btn-sm" onClick={() => askDial(host, 'connect')}>
            连接
          </button>
        </div>
        {test ? (
          <div className="row small" style={{ gap: 6 }}>
            {test.ok ? (
              <>
                <Tag kind="ok">{test.transport ?? 'ssh'}</Tag>
                <span className="faint mono">{test.elapsedMs ?? 0}ms</span>
                {test.uname ? (
                  <span className="faint truncate" title={test.uname}>
                    {test.uname}
                  </span>
                ) : null}
              </>
            ) : (
              <Tag kind="bad">{test.error ?? '连接失败'}</Tag>
            )}
          </div>
        ) : null}
      </div>
    )
  }

  return (
    <div className="split split-wide" style={{ height: 'calc(100vh - 122px)' }}>
      <div className="pane col" style={{ gap: 8, paddingRight: 4 }}>
        <input
          className="input"
          placeholder="搜索名称 / 地址 / 标签"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <div className="row" style={{ gap: 6 }}>
          <button className="btn btn-primary btn-sm" style={{ flex: 1 }} onClick={() => openHostEditor(null)}>
            新建主机
          </button>
          <button className="btn btn-sm" onClick={() => setKeysOpen(true)}>
            密钥 {keys.length}
          </button>
        </div>
        <div className="row small faint">
          <span>{hosts.length} 台主机</span>
          <div className="spacer" />
          <button className="btn btn-ghost btn-sm" onClick={() => void refreshAll()}>
            刷新
          </button>
        </div>

        {loading ? (
          <Spinner label="加载主机列表" />
        ) : groups.length === 0 ? (
          <Empty
            icon="⌘"
            title={hosts.length > 0 ? '没有匹配的主机' : '还没有主机'}
            hint={hosts.length > 0 ? '换个关键词试试' : '点击“新建主机”添加第一台'}
          />
        ) : (
          groups.map(([group, items]) => (
            <div key={group} className="col" style={{ gap: 6 }}>
              <div className="small faint" style={{ padding: '4px 2px 0' }}>
                {group} · {items.length}
              </div>
              <div className="list" style={{ gap: 6 }}>
                {items.map(renderHost)}
              </div>
            </div>
          ))
        )}

        <div className="divider" />
        <div className="row small faint">
          <span>活动会话</span>
          <div className="spacer" />
          <span>{sessions.length}</span>
        </div>
        {sessions.length === 0 ? (
          <div className="small faint">暂无会话</div>
        ) : (
          <div className="list" style={{ gap: 6 }}>
            {sessions.map((s) => (
              <div
                key={s.id}
                className={`file-row ${s.id === activeId ? 'selected' : ''}`}
                onClick={() => setActiveId(s.id)}
                title={`${s.user}@${s.address}`}
              >
                <span className="file-icon">▸</span>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="truncate">{s.name || s.hostId}</div>
                  <div className="small faint mono truncate">
                    {s.user}@{s.address} · {s.transport || 'ssh'}
                  </div>
                </div>
                <button
                  className="btn btn-ghost btn-sm"
                  title="断开"
                  onClick={(e) => {
                    e.stopPropagation()
                    void disconnect(s.id)
                  }}
                >
                  ✕
                </button>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="pane" style={{ display: 'flex', flexDirection: 'column', gap: 10, paddingRight: 2 }}>
        {!activeSession ? (
          <div className="card">
            <Empty icon="⌘" title="还没有活动的 SSH 会话" hint="在左侧选择主机点击“连接”，或先新建一台主机" />
            <div className="row" style={{ justifyContent: 'center', gap: 8 }}>
              <button className="btn" onClick={() => openHostEditor(null)}>
                新建主机
              </button>
              <button className="btn" onClick={() => setKeysOpen(true)}>
                密钥管理
              </button>
            </div>
          </div>
        ) : (
          <>
            <div className="card" style={{ padding: '10px 14px' }}>
              <div className="row">
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="row" style={{ gap: 8 }}>
                    <span style={{ fontWeight: 600 }}>{activeSession.name || activeSession.hostId}</span>
                    <Tag kind="info">{activeSession.transport || 'ssh'}</Tag>
                    {activeSession.chain && activeSession.chain.length > 0 ? (
                      <Tag kind="warn">跳板 {activeSession.chain.length} 跳</Tag>
                    ) : null}
                  </div>
                  <div className="small faint mono truncate">
                    {activeSession.user}@{activeSession.address} · 建立于 {formatTime(activeSession.openedAt)}
                  </div>
                </div>
                <button className="btn btn-sm" onClick={() => void disconnect(activeSession.id)}>
                  断开
                </button>
              </div>
            </div>

            <Tabs tabs={TABS} value={tab} onChange={setTab} />

            {tab === 'sftp' ? <SftpPane sessionId={sessionId} /> : null}
            {tab === 'term' ? <TerminalPane sessionId={sessionId} /> : null}
            {tab === 'cmd' ? <CommandPane sessionId={sessionId} /> : null}
          </>
        )}
      </div>

      {hostEditorOpen ? (
        <HostEditorModal
          key={editingHost?.id ?? 'new-host'}
          initial={editingHost}
          hosts={hosts}
          keys={keys}
          onClose={closeHostEditor}
          onSaved={async (saved) => {
            closeHostEditor()
            await refreshAll()
            toast.push(`已保存 ${saved.name}`)
          }}
        />
      ) : null}

      {keysOpen ? (
        <KeysModal
          onClose={() => setKeysOpen(false)}
          onChanged={async () => {
            await loadKeys()
          }}
        />
      ) : null}

      {dial ? (
        <Modal
          title={dial.purpose === 'test' ? `测试连接 · ${dial.host.name}` : `连接 · ${dial.host.name}`}
          onClose={() => setDial(null)}
          footer={
            <>
              <button className="btn" onClick={() => setDial(null)}>
                取消
              </button>
              <button className="btn btn-primary" disabled={dialBusy} onClick={() => void submitDial()}>
                {dialBusy ? '处理中…' : dial.purpose === 'test' ? '开始测试' : '连接'}
              </button>
            </>
          }
        >
          <div className="small faint mono truncate">
            {dial.host.user}@{dial.host.address}:{dial.host.port} · {authLabel(dial.host.authMode)} · {routeLabel(dial.host.route)}
          </div>
          {dial.host.authMode === 'key' ? (
            <Field label="密钥口令" hint="若该密钥没有口令可留空；留空时使用已保存的口令">
              <input
                className="input"
                type="password"
                autoFocus
                placeholder="留空则使用已保存口令"
                value={dial.passphrase}
                onChange={(e) => setDial({ ...dial, passphrase: e.target.value })}
              />
            </Field>
          ) : dial.host.authMode === 'password' || dial.host.authMode === 'keyboard-interactive' ? (
            <Field label="登录密码" hint="留空则使用主机中已保存的密码">
              <input
                className="input"
                type="password"
                autoFocus
                placeholder="留空则使用已保存密码"
                value={dial.password}
                onChange={(e) => setDial({ ...dial, password: e.target.value })}
              />
            </Field>
          ) : (
            <div className="small faint">该认证方式（{authLabel(dial.host.authMode)}）无需额外凭据，直接继续即可。</div>
          )}
        </Modal>
      ) : null}

      {pendingHostDelete ? (
        <Confirm
          title="删除主机"
          danger
          message={`确认删除主机“${pendingHostDelete.name}”吗？已保存的凭据会一并移除。`}
          onCancel={() => setPendingHostDelete(null)}
          onConfirm={() => {
            const host = pendingHostDelete
            setPendingHostDelete(null)
            void removeHost(host)
          }}
        />
      ) : null}
    </div>
  )
}

/* ---------------- host editor ---------------- */

function HostEditorModal({
  initial,
  hosts,
  keys,
  onClose,
  onSaved,
}: {
  initial: SSHHost | null
  hosts: SSHHost[]
  keys: SSHKey[]
  onClose: () => void
  onSaved: (host: SSHHost) => Promise<void> | void
}) {
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const [draft, setDraft] = useState(() => ({
    name: initial?.name ?? '',
    address: initial?.address ?? '',
    port: initial?.port ?? 22,
    user: initial?.user ?? 'root',
    authMode: initial?.authMode ?? 'key',
    keyId: initial?.keyId ?? '',
    password: '',
    route: initial?.route ?? 'direct',
    jumpIds: initial?.jumpIds ?? [],
    relayTarget: initial?.relayTarget ?? '',
    group: initial?.group ?? '',
    notes: initial?.notes ?? '',
    tags: (initial?.tags ?? []).join(', '),
    hostKeyFingerprint: initial?.hostKeyFingerprint ?? '',
  }))

  const patch = (next: Partial<typeof draft>) => setDraft((cur) => ({ ...cur, ...next }))

  const jumpCandidates = hosts.filter((h) => !!h.id && h.id !== initial?.id)

  const toggleJump = (id: string) => {
    setDraft((cur) => ({
      ...cur,
      jumpIds: cur.jumpIds.includes(id) ? cur.jumpIds.filter((x) => x !== id) : [...cur.jumpIds, id],
    }))
  }

  const save = async () => {
    if (!draft.name.trim() || !draft.address.trim()) {
      toast.push('请填写主机名称与地址', 'warn')
      return
    }
    if (draft.route === 'jump' && draft.jumpIds.length === 0) {
      toast.push('跳板路由至少需要选择一台跳板机', 'warn')
      return
    }
    setBusy(true)
    try {
      const tags = splitTags(draft.tags)
      const payload: SSHHost = {
        id: initial?.id,
        name: draft.name.trim(),
        address: draft.address.trim(),
        port: Number(draft.port) || 22,
        user: draft.user.trim() || 'root',
        authMode: draft.authMode,
        keyId: draft.authMode === 'key' ? draft.keyId || undefined : undefined,
        password: draft.password || undefined,
        route: draft.route,
        jumpIds: draft.route === 'jump' && draft.jumpIds.length > 0 ? draft.jumpIds : undefined,
        relayTarget: draft.route === 'relay' ? draft.relayTarget.trim() || undefined : undefined,
        group: draft.group.trim() || undefined,
        notes: draft.notes.trim() || undefined,
        tags: tags.length > 0 ? tags : undefined,
        hostKeyFingerprint: draft.hostKeyFingerprint.trim() || undefined,
        lastUsed: initial?.lastUsed,
        lastTransport: initial?.lastTransport,
      }
      const saved = await api.SaveSSHHost(payload)
      await onSaved(saved)
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      wide
      title={initial ? `编辑主机 · ${initial.name}` : '新建 SSH 主机'}
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
      <div className="grid-2">
        <Field label="名称">
          <input className="input" value={draft.name} onChange={(e) => patch({ name: e.target.value })} placeholder="例如 生产服务器" />
        </Field>
        <Field label="分组" hint="用于左侧列表分组，留空归入“未分组”">
          <input className="input" value={draft.group} onChange={(e) => patch({ group: e.target.value })} placeholder="默认" />
        </Field>
      </div>

      <div className="grid-3">
        <Field label="地址">
          <input className="input mono" value={draft.address} onChange={(e) => patch({ address: e.target.value })} placeholder="192.168.1.10" />
        </Field>
        <Field label="端口">
          <input
            className="input"
            type="number"
            min={1}
            max={65535}
            value={draft.port}
            onChange={(e) => patch({ port: Number(e.target.value) || 22 })}
          />
        </Field>
        <Field label="用户名">
          <input className="input mono" value={draft.user} onChange={(e) => patch({ user: e.target.value })} placeholder="root" />
        </Field>
      </div>

      <div className="grid-2">
        <Field label="认证方式">
          <select className="select" value={draft.authMode} onChange={(e) => patch({ authMode: e.target.value })}>
            <option value="key">密钥</option>
            <option value="password">密码</option>
            <option value="keyboard-interactive">键盘交互</option>
            <option value="agent">SSH Agent</option>
            <option value="none">无认证</option>
          </select>
        </Field>
        {draft.authMode === 'key' ? (
          <Field label="密钥" hint={keys.length === 0 ? '还没有密钥，请先在“密钥”里生成或导入' : undefined}>
            <select className="select" value={draft.keyId} onChange={(e) => patch({ keyId: e.target.value })}>
              <option value="">未选择</option>
              {keys.map((k) => (
                <option key={k.id} value={k.id}>
                  {k.name} · {k.type}
                  {k.bits ? ` ${k.bits}` : ''}
                </option>
              ))}
            </select>
          </Field>
        ) : (
          <Field label="密码" hint="保存在本机加密存储；留空会清除已保存的密码">
            <input
              className="input"
              type="password"
              value={draft.password}
              onChange={(e) => patch({ password: e.target.value })}
              placeholder="登录密码"
            />
          </Field>
        )}
      </div>

      <div className="divider" />

      <div className="grid-2">
        <Field label="连接路由">
          <select className="select" value={draft.route} onChange={(e) => patch({ route: e.target.value })}>
            <option value="direct">直连</option>
            <option value="relay">服务器中转</option>
            <option value="jump">跳板机链</option>
          </select>
        </Field>
        {draft.route === 'relay' ? (
          <Field label="中转目标" hint="中转目标 host:port，留空则使用主机地址">
            <input
              className="input mono"
              value={draft.relayTarget}
              onChange={(e) => patch({ relayTarget: e.target.value })}
              placeholder="10.0.0.5:22"
            />
          </Field>
        ) : draft.route === 'jump' ? (
          <Field label="跳板机" hint="按顺序经过这些主机，可多选">
            <div className="col" style={{ gap: 4, maxHeight: 150, overflow: 'auto' }}>
              {jumpCandidates.length === 0 ? (
                <span className="small faint">没有其它主机可选</span>
              ) : (
                jumpCandidates.map((h) => (
                  <label key={h.id} className="row small" style={{ gap: 6, cursor: 'pointer' }}>
                    <input
                      type="checkbox"
                      checked={h.id ? draft.jumpIds.includes(h.id) : false}
                      onChange={() => h.id && toggleJump(h.id)}
                    />
                    <span className="truncate">
                      {h.name} <span className="faint mono">{h.address}</span>
                    </span>
                  </label>
                ))
              )}
            </div>
          </Field>
        ) : (
          <Field label="路由说明" hint="直连：从本机直接连接目标主机">
            <input className="input" disabled value="无需额外配置" />
          </Field>
        )}
      </div>

      <div className="grid-2">
        <Field label="标签" hint="逗号分隔，例如 生产, web">
          <input className="input" value={draft.tags} onChange={(e) => patch({ tags: e.target.value })} placeholder="生产, web" />
        </Field>
        <Field label="主机指纹（可选）" hint="高级选项，填写 SHA256:... 可固定服务器公钥指纹">
          <input
            className="input mono"
            value={draft.hostKeyFingerprint}
            onChange={(e) => patch({ hostKeyFingerprint: e.target.value })}
            placeholder="SHA256:…"
          />
        </Field>
      </div>

      <Field label="备注">
        <textarea className="textarea" value={draft.notes} onChange={(e) => patch({ notes: e.target.value })} placeholder="备注信息" />
      </Field>
    </Modal>
  )
}

/* ---------------- keys manager ---------------- */

function KeysModal({ onClose, onChanged }: { onClose: () => void; onChanged: () => Promise<void> | void }) {
  const toast = useToast()
  const [keys, setKeys] = useState<SSHKey[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [mode, setMode] = useState<'none' | 'generate' | 'import'>('none')
  const [exported, setExported] = useState<{ name: string; body: string } | null>(null)
  const [pendingDelete, setPendingDelete] = useState<SSHKey | null>(null)
  const [gen, setGen] = useState({ name: '', type: 'ed25519', bits: 256, comment: '', passphrase: '' })
  const [imp, setImp] = useState({ name: '', privateKey: '', passphrase: '', comment: '' })

  const reload = useCallback(async () => {
    try {
      setKeys(await api.ListSSHKeys())
    } catch (err) {
      toast.error(err)
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    void reload()
  }, [reload])

  const bitOptions = gen.type === 'rsa' ? [2048, 3072, 4096] : gen.type === 'ecdsa' ? [256, 384, 521] : []

  const changeType = (type: string) => {
    setGen((cur) => ({ ...cur, type, bits: type === 'rsa' ? 3072 : type === 'ecdsa' ? 256 : 256 }))
  }

  const afterChange = async () => {
    await reload()
    await onChanged()
  }

  const generate = async () => {
    if (!gen.name.trim()) {
      toast.push('请填写密钥名称', 'warn')
      return
    }
    setBusy(true)
    try {
      const key = await api.GenerateSSHKey({
        name: gen.name.trim(),
        type: gen.type,
        bits: Number(gen.bits) || 0,
        comment: gen.comment.trim(),
        passphrase: gen.passphrase,
      })
      toast.push(`已生成密钥 ${key.name}`)
      setGen({ name: '', type: 'ed25519', bits: 256, comment: '', passphrase: '' })
      setMode('none')
      await afterChange()
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  const importKey = async () => {
    if (!imp.privateKey.trim()) {
      toast.push('请粘贴私钥内容', 'warn')
      return
    }
    setBusy(true)
    try {
      const key = await api.ImportSSHKey({
        name: imp.name.trim(),
        privateKey: imp.privateKey.trim(),
        passphrase: imp.passphrase,
        comment: imp.comment.trim(),
      })
      toast.push(`已导入密钥 ${key.name}`)
      setImp({ name: '', privateKey: '', passphrase: '', comment: '' })
      setMode('none')
      await afterChange()
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  const copyPublic = async (key: SSHKey) => {
    const ok = await copyText(key.publicKey)
    if (ok) toast.push('公钥已复制到剪贴板')
    else toast.push('复制失败，请手动选择复制', 'warn')
  }

  const exportPrivate = async (key: SSHKey) => {
    try {
      const body = await api.ExportSSHPrivateKey(key.id)
      setExported({ name: key.name, body })
    } catch (err) {
      toast.error(err)
    }
  }

  const removeKey = async (key: SSHKey) => {
    try {
      await api.DeleteSSHKey(key.id)
      toast.push(`已删除密钥 ${key.name}`)
      await afterChange()
    } catch (err) {
      toast.error(err)
    }
  }

  return (
    <Modal
      wide
      title="SSH 密钥管理"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            关闭
          </button>
          <button className="btn" onClick={() => setMode(mode === 'import' ? 'none' : 'import')}>
            导入密钥
          </button>
          <button className="btn btn-primary" onClick={() => setMode(mode === 'generate' ? 'none' : 'generate')}>
            生成密钥
          </button>
        </>
      }
    >
      {mode === 'generate' ? (
        <div className="card" style={{ background: 'var(--bg-sunken)' }}>
          <h3 className="card-title">生成新密钥</h3>
          <div className="grid-2">
            <Field label="名称">
              <input
                className="input"
                value={gen.name}
                onChange={(e) => setGen({ ...gen, name: e.target.value })}
                placeholder="例如 生产服务器密钥"
              />
            </Field>
            <Field label="类型">
              <select className="select" value={gen.type} onChange={(e) => changeType(e.target.value)}>
                <option value="ed25519">Ed25519（推荐）</option>
                <option value="rsa">RSA</option>
                <option value="ecdsa">ECDSA</option>
              </select>
            </Field>
          </div>
          <div className="grid-3">
            {bitOptions.length > 0 ? (
              <Field label="位数">
                <select className="select" value={gen.bits} onChange={(e) => setGen({ ...gen, bits: Number(e.target.value) })}>
                  {bitOptions.map((b) => (
                    <option key={b} value={b}>
                      {b}
                    </option>
                  ))}
                </select>
              </Field>
            ) : (
              <Field label="位数" hint="Ed25519 固定 256 位">
                <input className="input" disabled value="256" />
              </Field>
            )}
            <Field label="注释（可选）">
              <input className="input" value={gen.comment} onChange={(e) => setGen({ ...gen, comment: e.target.value })} placeholder="user@host" />
            </Field>
            <Field label="口令（可选）" hint="为私钥加密，连接时需要输入">
              <input
                className="input"
                type="password"
                value={gen.passphrase}
                onChange={(e) => setGen({ ...gen, passphrase: e.target.value })}
              />
            </Field>
          </div>
          <div className="row">
            <div className="spacer" />
            <button className="btn" onClick={() => setMode('none')}>
              取消
            </button>
            <button className="btn btn-primary" disabled={busy} onClick={() => void generate()}>
              {busy ? '生成中…' : '生成'}
            </button>
          </div>
        </div>
      ) : null}

      {mode === 'import' ? (
        <div className="card" style={{ background: 'var(--bg-sunken)' }}>
          <h3 className="card-title">导入已有私钥</h3>
          <div className="grid-2">
            <Field label="名称（可选）">
              <input className="input" value={imp.name} onChange={(e) => setImp({ ...imp, name: e.target.value })} placeholder="自动识别" />
            </Field>
            <Field label="口令（可选）" hint="私钥加密时填写">
              <input
                className="input"
                type="password"
                value={imp.passphrase}
                onChange={(e) => setImp({ ...imp, passphrase: e.target.value })}
              />
            </Field>
          </div>
          <Field label="私钥内容" hint="支持 OpenSSH / PEM 格式，粘贴后即保存在本机加密存储">
            <textarea
              className="textarea"
              style={{ minHeight: 140 }}
              value={imp.privateKey}
              onChange={(e) => setImp({ ...imp, privateKey: e.target.value })}
              placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
            />
          </Field>
          <Field label="注释（可选）">
            <input className="input" value={imp.comment} onChange={(e) => setImp({ ...imp, comment: e.target.value })} />
          </Field>
          <div className="row">
            <div className="spacer" />
            <button className="btn" onClick={() => setMode('none')}>
              取消
            </button>
            <button className="btn btn-primary" disabled={busy} onClick={() => void importKey()}>
              {busy ? '导入中…' : '导入'}
            </button>
          </div>
        </div>
      ) : null}

      {loading ? (
        <Spinner label="加载密钥" />
      ) : keys.length === 0 ? (
        <Empty icon="🔑" title="还没有密钥" hint="可以生成一对新密钥，或导入已有私钥" />
      ) : (
        <div className="list">
          {keys.map((key) => (
            <div key={key.id} className="list-row" style={{ flexDirection: 'column', alignItems: 'stretch', gap: 8 }}>
              <div className="row">
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="row" style={{ gap: 8 }}>
                    <span className="name truncate">{key.name}</span>
                    <Tag kind="info">
                      {key.type}
                      {key.bits ? ` ${key.bits}` : ''}
                    </Tag>
                    {key.comment ? (
                      <span className="small faint truncate" title={key.comment}>
                        {key.comment}
                      </span>
                    ) : null}
                  </div>
                  <div className="small faint mono truncate" title={key.fingerprint}>
                    {key.fingerprint}
                  </div>
                </div>
                <span className="small faint nowrap">{relativeTime(key.createdAt)}</span>
              </div>
              <div className="row" style={{ gap: 8 }}>
                <button className="btn btn-sm" onClick={() => void copyPublic(key)}>
                  复制公钥
                </button>
                <button className="btn btn-sm" onClick={() => void exportPrivate(key)}>
                  导出私钥
                </button>
                <div className="spacer" />
                <button className="btn btn-danger btn-sm" onClick={() => setPendingDelete(key)}>
                  删除
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {exported ? (
        <Modal
          title={`私钥 · ${exported.name}`}
          onClose={() => setExported(null)}
          footer={
            <>
              <button className="btn" onClick={() => setExported(null)}>
                关闭
              </button>
              <button
                className="btn btn-primary"
                onClick={async () => {
                  const ok = await copyText(exported.body)
                  if (ok) toast.push('私钥已复制到剪贴板')
                  else toast.push('复制失败，请手动选择复制', 'warn')
                }}
              >
                复制
              </button>
            </>
          }
        >
          <div className="small" style={{ color: 'var(--warn)' }}>
            私钥等同于登录凭据，请勿泄露或提交到代码仓库。
          </div>
          <textarea
            className="textarea mono"
            style={{ minHeight: 240 }}
            readOnly
            value={exported.body}
            onClick={(e) => e.currentTarget.select()}
          />
        </Modal>
      ) : null}

      {pendingDelete ? (
        <Confirm
          title="删除密钥"
          danger
          message={`确认删除密钥“${pendingDelete.name}”吗？使用该密钥的主机将无法再登录。`}
          onCancel={() => setPendingDelete(null)}
          onConfirm={() => {
            const key = pendingDelete
            setPendingDelete(null)
            void removeKey(key)
          }}
        />
      ) : null}
    </Modal>
  )
}

/* ---------------- SFTP ---------------- */

function SftpPane({ sessionId }: { sessionId: string }) {
  const toast = useToast()
  const [dir, setDir] = useState('')
  const [files, setFiles] = useState<RemoteFile[]>([])
  const [selected, setSelected] = useState<RemoteFile | null>(null)
  const [loading, setLoading] = useState(false)
  const [preview, setPreview] = useState<{ name: string; body: string } | null>(null)
  const [pendingRemove, setPendingRemove] = useState<RemoteFile | null>(null)
  const [prompt, setPrompt] = useState<PromptState | null>(null)
  const [xfers, setXfers] = useState<Record<string, XferState>>({})
  const dirRef = useRef('')
  const reloadRef = useRef<() => void>(() => {})

  const openDir = useCallback(
    async (target: string) => {
      setLoading(true)
      try {
        const list = await api.SFTPList(sessionId, target)
        setFiles(list)
        setDir(target)
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
      void openDir(dirRef.current)
    }
  }, [openDir])

  useEffect(() => {
    let disposed = false
    void (async () => {
      try {
        const home = await api.SFTPHome(sessionId)
        if (disposed) return
        await openDir(home || '/')
      } catch (err) {
        toast.error(err)
      }
    })()
    return () => {
      disposed = true
    }
  }, [sessionId, openDir, toast])

  useEffect(() => {
    const offProgress = onEvent<TransferProgress>('transfer:progress', (ev) => {
      if (!ev?.key || typeof ev.kind !== 'string' || !ev.kind.startsWith('sftp-')) return
      const p = ev.progress
      setXfers((prev) => ({
        ...prev,
        [ev.key]: {
          key: ev.key,
          label: ev.kind === 'sftp-upload' ? '上传' : '下载',
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
      if (!ev?.key || typeof ev.kind !== 'string' || !ev.kind.startsWith('sftp-')) return
      const label = ev.kind === 'sftp-upload' ? '上传' : '下载'
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
      if (ev.kind === 'sftp-upload') reloadRef.current()
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
    const name = await ask('新建文件夹', '文件夹名称')
    if (!name) return
    try {
      await api.SFTPMkdir(sessionId, joinPath(dir, name))
      toast.push('文件夹已创建')
      await openDir(dir)
    } catch (err) {
      toast.error(err)
    }
  }

  const upload = async () => {
    try {
      const local = await api.PickFile('选择要上传的文件')
      if (!local) return
      const remote = joinPath(dir, basename(local))
      const res = await api.SFTPUpload(sessionId, local, remote)
      toast.push(`已上传 ${basename(res.path ?? remote)}`)
      await openDir(dir)
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
      toast.push('请选择文件而不是文件夹', 'warn')
      return
    }
    try {
      const target = await api.PickDirectory('选择保存目录')
      if (!target) return
      const local = joinPath(target, selected.name || basename(selected.path))
      const res = await api.SFTPDownload(sessionId, selected.path, local)
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
      await api.SFTPRename(sessionId, selected.path, joinPath(dir, name))
      toast.push('已重命名')
      await openDir(dir)
    } catch (err) {
      toast.error(err)
    }
  }

  const readFile = async (file: RemoteFile) => {
    try {
      const body = await api.SFTPReadFile(sessionId, file.path)
      setPreview({ name: file.name, body })
    } catch (err) {
      toast.error(err)
    }
  }

  const remove = async (file: RemoteFile) => {
    try {
      await api.SFTPRemove(sessionId, file.path, file.isDir)
      toast.push('已删除')
      await openDir(dir)
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
        <button className="btn btn-sm" onClick={() => void openDir(dir)} disabled={loading}>
          刷新
        </button>
        <button className="btn btn-sm" onClick={() => void mkdir()}>
          新建文件夹
        </button>
        <button className="btn btn-sm" onClick={() => void upload()}>
          上传
        </button>
        <button className="btn btn-sm" onClick={() => void download()} disabled={!selected || selected.isDir}>
          下载选中
        </button>
        <button className="btn btn-sm" onClick={() => void rename()} disabled={!selected}>
          重命名
        </button>
        <button className="btn btn-sm" onClick={() => (selected ? void readFile(selected) : undefined)} disabled={!selected || selected.isDir}>
          预览
        </button>
        <button className="btn btn-danger btn-sm" onClick={() => setPendingRemove(selected)} disabled={!selected}>
          删除
        </button>
        <div className="spacer" />
        {loading ? <Spinner /> : <span className="small faint">{files.length} 项</span>}
      </div>

      <div className="crumbs">
        <span className="crumb" onClick={() => void openDir('/')}>
          /
        </span>
        {crumbs.map((c) => (
          <span key={c.path} className="row" style={{ gap: 0 }}>
            <span className="faint">›</span>
            <span className="crumb" onClick={() => void openDir(c.path)}>
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
          <div className="file-row" onClick={() => void openDir(parentOf(dir))}>
            <span className="file-icon">↰</span>
            <span className="file-name dim">..</span>
          </div>
        ) : null}
        {files.length === 0 && !loading ? (
          <Empty icon="📂" title="这个目录是空的" hint="可以上传文件或新建文件夹" />
        ) : (
          files.map((file) => (
            <div
              key={file.path || file.name}
              className={`file-row ${selected?.path === file.path ? 'selected' : ''}`}
              title={file.path}
              onClick={() => setSelected(file)}
              onDoubleClick={() => (file.isDir ? void openDir(file.path) : void readFile(file))}
            >
              <span className="file-icon">{file.isDir ? '📁' : file.isLink ? '🔗' : '📄'}</span>
              <span className="file-name">{file.name}</span>
              {file.isLink ? <Tag>链接</Tag> : null}
              <span className="file-size">{file.isDir ? '—' : formatBytes(file.size)}</span>
              <span className="small faint nowrap" style={{ width: 132, textAlign: 'right' }}>
                {formatTime(file.modTime)}
              </span>
            </div>
          ))
        )}
      </div>

      {preview ? (
        <Modal
          wide
          title={`预览 · ${preview.name}`}
          onClose={() => setPreview(null)}
          footer={
            <button className="btn" onClick={() => setPreview(null)}>
              关闭
            </button>
          }
        >
          <textarea className="textarea mono" style={{ minHeight: 320 }} readOnly value={preview.body} />
        </Modal>
      ) : null}

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

/* ---------------- terminal ---------------- */

const TERM_KEYS: Record<string, string> = {
  Enter: '\r',
  Backspace: '\x7f',
  Tab: '\t',
  Escape: '\x1b',
  ArrowUp: '\x1b[A',
  ArrowDown: '\x1b[B',
  ArrowRight: '\x1b[C',
  ArrowLeft: '\x1b[D',
  Home: '\x1b[H',
  End: '\x1b[F',
  Delete: '\x1b[3~',
  PageUp: '\x1b[5~',
  PageDown: '\x1b[6~',
}

function TerminalPane({ sessionId }: { sessionId: string }) {
  const toast = useToast()
  const [termId, setTermId] = useState<string | null>(null)
  const [output, setOutput] = useState('')
  const [closed, setClosed] = useState(false)
  const idRef = useRef<string | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const boxRef = useRef<HTMLPreElement | null>(null)

  useEffect(() => {
    let disposed = false
    let opened = ''
    void (async () => {
      try {
        const width = boxRef.current?.clientWidth ?? 820
        const cols = Math.max(40, Math.min(240, Math.floor(width / 7.4)))
        const id = await api.OpenTerminal(sessionId, cols, 30)
        if (disposed) {
          void api.CloseTerminal(id)
          return
        }
        opened = id
        idRef.current = id
        setTermId(id)
        setOutput('')
        setClosed(false)
        window.setTimeout(() => inputRef.current?.focus(), 80)
      } catch (err) {
        toast.error(err)
        setClosed(true)
      }
    })()
    return () => {
      disposed = true
      idRef.current = null
      if (opened) void api.CloseTerminal(opened)
    }
  }, [sessionId, toast])

  useEffect(() => {
    const offData = onEvent<{ id: string; data: string }>('ssh:terminal:data', (ev) => {
      if (!ev?.data || ev.id !== idRef.current) return
      setOutput((prev) => {
        const next = prev + ev.data
        return next.length > 200_000 ? next.slice(next.length - 200_000) : next
      })
    })
    const offClosed = onEvent<{ id: string; error?: string }>('ssh:terminal:closed', (ev) => {
      if (ev?.id !== idRef.current) return
      idRef.current = null
      setTermId(null)
      setClosed(true)
      toast.push(ev.error ? `终端已关闭：${ev.error}` : '终端已关闭', ev.error ? 'warn' : 'info')
    })
    return () => {
      offData()
      offClosed()
    }
  }, [toast])

  useEffect(() => {
    const box = boxRef.current
    if (box) box.scrollTop = box.scrollHeight
  }, [output])

  const send = (data: string) => {
    const id = idRef.current
    if (!id) return
    void api.WriteTerminal(id, data).catch((err) => toast.error(err))
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (!idRef.current || closed) return
    if (e.ctrlKey && !e.altKey && e.key.toLowerCase() === 'c') {
      e.preventDefault()
      send('\x03')
      return
    }
    const mapped = TERM_KEYS[e.key]
    if (mapped) {
      e.preventDefault()
      send(mapped)
      return
    }
    if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault()
      send(e.key)
    }
  }

  const closeTerm = () => {
    const id = idRef.current
    idRef.current = null
    setTermId(null)
    setClosed(true)
    if (id) void api.CloseTerminal(id).catch((err) => toast.error(err))
  }

  return (
    <div className="col" style={{ gap: 10, minHeight: 0 }}>
      <div className="row">
        <span className="small faint">会话内直接输入命令；方向键、Tab、Ctrl+C 均会转发到远端。</span>
        <div className="spacer" />
        <Tag kind={closed ? 'bad' : termId ? 'ok' : 'info'}>{closed ? '已结束' : termId ? '已连接' : '打开中…'}</Tag>
        <button className="btn btn-sm" onClick={closeTerm} disabled={!termId}>
          关闭终端
        </button>
      </div>
      <pre className="term" ref={boxRef} style={{ margin: 0 }} onClick={() => inputRef.current?.focus()}>
        {output || (closed ? '（终端已结束）' : '正在建立终端会话…')}
        <input
          ref={inputRef}
          className="mono"
          value=""
          onChange={() => {}}
          onKeyDown={onKeyDown}
          disabled={!termId || closed}
          spellCheck={false}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          aria-label="终端输入"
          style={{ caretColor: 'var(--accent)' }}
        />
      </pre>
      <div className="small faint">提示：如终端无响应，可关闭后重新打开；断开连接时终端会自动关闭。</div>
    </div>
  )
}

/* ---------------- command runner ---------------- */

const QUICK_COMMANDS = ['uname -a', 'uptime', 'df -h', 'free -m', 'whoami', 'pwd']

function CommandPane({ sessionId }: { sessionId: string }) {
  const toast = useToast()
  const [command, setCommand] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ command: string; stdout: string; stderr: string; exitCode: number } | null>(null)

  const run = async () => {
    const cmd = command.trim()
    if (!cmd) return
    setBusy(true)
    try {
      const res = await api.RunSSHCommand(sessionId, cmd, 60)
      setResult({ command: cmd, stdout: res.stdout ?? '', stderr: res.stderr ?? '', exitCode: res.exitCode ?? 0 })
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="col" style={{ gap: 10, minHeight: 0 }}>
      <div className="row" style={{ gap: 8 }}>
        <input
          className="input mono"
          style={{ flex: 1 }}
          placeholder="输入命令，例如 uname -a"
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void run()
          }}
        />
        <button className="btn btn-primary" disabled={busy || !command.trim()} onClick={() => void run()}>
          {busy ? '执行中…' : '运行'}
        </button>
      </div>
      <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
        <span className="small faint">常用：</span>
        {QUICK_COMMANDS.map((c) => (
          <button key={c} className="btn btn-ghost btn-sm mono" onClick={() => setCommand(c)}>
            {c}
          </button>
        ))}
        <div className="spacer" />
        {result ? (
          <Tag kind={result.exitCode === 0 ? 'ok' : 'bad'}>退出码 {result.exitCode}</Tag>
        ) : (
          <span className="small faint">超时 60 秒</span>
        )}
      </div>
      <pre className="term" style={{ height: 380, margin: 0 }}>
        {result
          ? `$ ${result.command}\n${result.stdout}${result.stderr ? `\n[stderr]\n${result.stderr}` : ''}\n[exit ${result.exitCode}]`
          : '运行一条命令后，这里会显示 stdout / stderr 与退出码。'}
      </pre>
    </div>
  )
}
