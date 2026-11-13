import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, onEvent, type LocalListing, type RemoteEntry } from '../lib/api'
import { Confirm, Empty, Field, Modal, Progress, Spinner, Stat, Tabs, Tag, useToast } from '../components/ui'
import { basename, formatBytes, formatSpeed, formatTime, percent } from '../lib/format'
import type { PageProps } from '../App'

type TabKey = 'local' | 'cloud' | 'transfers'

const TABS: Array<{ id: TabKey; label: string }> = [
  { id: 'local', label: '本地文件' },
  { id: 'cloud', label: '云端文件' },
  { id: 'transfers', label: '传输记录' },
]

/** 云传输的进度载荷：{key, kind, done, total} */
interface CloudProgressPayload {
  key?: string
  kind?: string
  done?: number
  total?: number
  path?: string
}

/** SFTP / FTP 的进度载荷：{key, kind, progress:{done,total,percent,speedBps,path}} */
interface StreamProgressPayload {
  key?: string
  kind?: string
  progress?: { done: number; total: number; percent: number; speedBps: number; path: string }
}

interface TransferDonePayload {
  key?: string
  kind?: string
  path?: string
  bytes?: number
}

interface TransferItem {
  key: string
  kind: string
  path: string
  done: number
  total: number
  finished: boolean
  bytes: number
  speedBps: number
}

interface Usage {
  usedBytes: number
  quotaBytes: number
}

export default function FilesPage({ serverStatus }: PageProps) {
  const toast = useToast()
  const connected = serverStatus?.connected ?? false

  const [tab, setTab] = useState<TabKey>('local')

  /* ---- 本地 ---- */
  const [local, setLocal] = useState<LocalListing | null>(null)
  const [localBusy, setLocalBusy] = useState(false)
  const [selected, setSelected] = useState<string[]>([])

  /* ---- 云端 ---- */
  const [cloudPath, setCloudPath] = useState('/')
  const [cloudEntries, setCloudEntries] = useState<RemoteEntry[]>([])
  const [cloudBusy, setCloudBusy] = useState(false)
  const [cloudSelected, setCloudSelected] = useState('')
  const [usage, setUsage] = useState<Usage | null>(null)

  const [mkdirOpen, setMkdirOpen] = useState(false)
  const [mkdirName, setMkdirName] = useState('')
  const [renameOpen, setRenameOpen] = useState(false)
  const [renameName, setRenameName] = useState('')
  const [removing, setRemoving] = useState<RemoteEntry | null>(null)

  /* ---- 传输记录 ---- */
  const [transfers, setTransfers] = useState<TransferItem[]>([])

  const loadLocal = useCallback(
    async (dir?: string) => {
      setLocalBusy(true)
      try {
        const listing = await api.ListLocal(dir ?? '')
        setLocal(listing)
        setSelected([])
      } catch (err) {
        toast.error(err)
      } finally {
        setLocalBusy(false)
      }
    },
    [toast],
  )

  const loadUsage = useCallback(async () => {
    try {
      setUsage(await api.ServerUsage())
    } catch {
      setUsage(null)
    }
  }, [])

  const loadCloud = useCallback(
    async (path: string) => {
      if (!connected) return
      setCloudBusy(true)
      try {
        const entries = await api.ListServerFiles(path)
        setCloudEntries(sortEntries(entries))
        setCloudPath(path)
        setCloudSelected('')
      } catch (err) {
        toast.error(err)
      } finally {
        setCloudBusy(false)
      }
    },
    [connected, toast],
  )

  useEffect(() => {
    void loadLocal()
  }, [loadLocal])

  useEffect(() => {
    if (tab !== 'cloud' || !connected) return
    void loadCloud(cloudPath)
    void loadUsage()
    // 仅在切到云端页 / 连接状态变化时拉取，路径变化由各操作自行刷新
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, connected, loadCloud, loadUsage])

  /* ---- 传输事件 ---- */
  useEffect(() => {
    const paintedAt: Record<string, number> = {}
    const offProgress = onEvent<CloudProgressPayload | StreamProgressPayload>('transfer:progress', (payload) => {
      const key = payload?.key
      if (!key) return
      // 进度回调可能非常密集，按 key 做 120ms 节流，避免频繁重渲染
      const now = Date.now()
      if (now - (paintedAt[key] ?? 0) < 120) return
      paintedAt[key] = now
      const stream = 'progress' in payload ? payload.progress : undefined
      const done = stream?.done ?? ('done' in payload ? payload.done : undefined) ?? 0
      const total = stream?.total ?? ('total' in payload ? payload.total : undefined) ?? 0
      const path = stream?.path ?? ('path' in payload ? payload.path : undefined) ?? ''
      const speedBps = stream?.speedBps ?? 0
      setTransfers((prev) =>
        mergeTransfer(prev, { key, kind: payload.kind ?? '', path, done, total, speedBps, finished: false }),
      )
    })

    const offDone = onEvent<TransferDonePayload>('transfer:done', (payload) => {
      const key = payload?.key
      if (!key) return
      setTransfers((prev) => {
        const current = prev.find((t) => t.key === key)
        const bytes = payload.bytes ?? current?.done ?? 0
        return mergeTransfer(prev, {
          key,
          kind: payload.kind ?? current?.kind ?? '',
          path: payload.path ?? current?.path ?? '',
          done: bytes,
          total: current?.total ?? bytes,
          bytes,
          finished: true,
          speedBps: 0,
        })
      })
    })

    return () => {
      offProgress()
      offDone()
    }
  }, [])

  /* ---- 本地操作 ---- */

  const toggleSelect = (path: string) => {
    setSelected((prev) => (prev.includes(path) ? prev.filter((p) => p !== path) : [...prev, path]))
  }

  const selectAll = () => {
    const entries = local?.entries ?? []
    setSelected(selected.length === entries.length ? [] : entries.map((e) => e.path))
  }

  const pickLocalDir = async () => {
    try {
      const dir = await api.PickDirectory('选择目录')
      if (dir) await loadLocal(dir)
    } catch (err) {
      toast.error(err)
    }
  }

  const openSelected = async (reveal: boolean) => {
    const target = selected[0]
    if (!target) return
    try {
      if (reveal) await api.RevealInExplorer(target)
      else await api.OpenPath(target)
    } catch (err) {
      toast.error(err)
    }
  }

  const uploadSelected = async () => {
    if (selected.length === 0) return
    if (!connected) {
      toast.push('请先在「同步服务器」页面连接服务器', 'warn')
      return
    }
    const remoteDir = cloudPath || '/'
    try {
      const res = await api.UploadToServer(selected, remoteDir)
      const results = res?.results ?? []
      const failed = results.filter((r) => typeof r.error === 'string' && r.error !== '').length
      if (failed > 0) {
        toast.push(`已上传 ${selected.length - failed} 个，${failed} 个失败`, 'warn')
      } else {
        toast.push(`已上传 ${selected.length} 个项目到 ${remoteDir}`)
      }
      setSelected([])
      void loadUsage()
      if (tab === 'cloud') void loadCloud(cloudPath)
    } catch (err) {
      toast.error(err)
    }
  }

  /* ---- 云端操作 ---- */

  const cloudEntry = useMemo(() => cloudEntries.find((e) => e.path === cloudSelected) ?? null, [cloudEntries, cloudSelected])

  const submitMkdir = async () => {
    const name = mkdirName.trim()
    if (!name) {
      toast.push('请输入文件夹名称', 'warn')
      return
    }
    try {
      await api.MkdirServerFile(serverJoin(cloudPath, name))
      setMkdirOpen(false)
      setMkdirName('')
      toast.push('文件夹已创建')
      await loadCloud(cloudPath)
    } catch (err) {
      toast.error(err)
    }
  }

  const submitRename = async () => {
    if (!cloudEntry) return
    const name = renameName.trim()
    if (!name) {
      toast.push('请输入新名称', 'warn')
      return
    }
    try {
      await api.RenameServerFile(cloudEntry.path, serverJoin(cloudPath, name))
      setRenameOpen(false)
      setRenameName('')
      toast.push('已重命名')
      await loadCloud(cloudPath)
    } catch (err) {
      toast.error(err)
    }
  }

  const submitRemove = async () => {
    const entry = removing
    setRemoving(null)
    if (!entry) return
    try {
      await api.RemoveServerFile(entry.path, entry.isDir)
      toast.push('已删除')
      await loadCloud(cloudPath)
      void loadUsage()
    } catch (err) {
      toast.error(err)
    }
  }

  const uploadLocalFile = async () => {
    try {
      const file = await api.PickFile('选择要上传的文件')
      if (!file) return
      const remoteDir = cloudPath || '/'
      await api.UploadToServer([file], remoteDir)
      toast.push(`已上传 ${basename(file)}`)
      await loadCloud(cloudPath)
      void loadUsage()
    } catch (err) {
      toast.error(err)
    }
  }

  const downloadCloud = async () => {
    if (!cloudEntry) return
    try {
      const dir = await api.PickDirectory('选择保存目录')
      if (!dir) return
      const res = await api.DownloadFromServer(cloudEntry.path, dir)
      toast.push(`已下载到 ${res.path}`)
      void loadUsage()
    } catch (err) {
      toast.error(err)
    }
  }

  /* ---- 渲染 ---- */

  const localEntries = useMemo(() => sortEntries(local?.entries ?? []), [local])
  const localCrumbs = useMemo(() => buildCrumbs(local?.path ?? '', local?.separator ?? '\\'), [local])
  const cloudCrumbs = useMemo(() => buildCloudCrumbs(cloudPath), [cloudPath])
  const usagePct = usage ? percent(usage.usedBytes, usage.quotaBytes) : 0

  return (
    <div>
      <Tabs tabs={TABS} value={tab} onChange={setTab} />

      {tab === 'local' && (
        <div className="card">
          <div className="row row-wrap">
            <div className="crumbs" style={{ flex: 1, minWidth: 240 }}>
              {localCrumbs.length === 0 ? <span className="faint">—</span> : null}
              {localCrumbs.map((c) => (
                <span key={c.path} className="row" style={{ gap: 2 }}>
                  <button className="crumb" onClick={() => void loadLocal(c.path)} title={c.path}>
                    {c.label}
                  </button>
                  <span className="faint">›</span>
                </span>
              ))}
            </div>
            <button className="btn btn-sm" onClick={() => void loadLocal(local?.home ?? '')} disabled={localBusy}>
              主页
            </button>
            <button
              className="btn btn-sm"
              onClick={() => void loadLocal(local?.parent ?? '')}
              disabled={localBusy || !local?.parent}
            >
              上一级
            </button>
            <button className="btn btn-sm" onClick={() => void loadLocal(local?.path)} disabled={localBusy}>
              刷新
            </button>
            <button className="btn btn-sm" onClick={pickLocalDir}>
              选择目录
            </button>
          </div>

          <div className="divider" />

          {selected.length > 0 ? (
            <div className="row row-wrap" style={{ marginBottom: 10 }}>
              <Tag kind="info">已选 {selected.length} 项</Tag>
              <button className="btn btn-sm btn-primary" onClick={uploadSelected}>
                上传到云端
              </button>
              <button className="btn btn-sm" onClick={() => void openSelected(false)}>
                打开
              </button>
              <button className="btn btn-sm" onClick={() => void openSelected(true)}>
                在资源管理器中显示
              </button>
              <div className="spacer" />
              <button className="btn btn-sm btn-ghost" onClick={() => setSelected([])}>
                取消选择
              </button>
            </div>
          ) : (
            <div className="row small faint" style={{ marginBottom: 10 }}>
              <span>{localEntries.length} 个项目 · 单击选择，双击进入文件夹</span>
              <div className="spacer" />
              {localEntries.length > 0 ? (
                <button className="btn btn-sm btn-ghost" onClick={selectAll}>
                  全选
                </button>
              ) : null}
            </div>
          )}

          {localBusy ? (
            <div style={{ padding: 20 }}>
              <Spinner label="读取目录" />
            </div>
          ) : localEntries.length === 0 ? (
            <Empty icon="▤" title="这个目录是空的" hint="换一个目录，或把文件放到这里后再刷新" />
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              {localEntries.map((entry) => {
                const isSelected = selected.includes(entry.path)
                return (
                  <div
                    key={entry.path}
                    className={`file-row ${isSelected ? 'selected' : ''}`}
                    onClick={() => toggleSelect(entry.path)}
                    onDoubleClick={() => entry.isDir && void loadLocal(entry.path)}
                    title={entry.path}
                  >
                    <span
                      className="small"
                      style={{ width: 14, color: isSelected ? 'var(--accent)' : 'var(--text-faint)' }}
                    >
                      {isSelected ? '☑' : '☐'}
                    </span>
                    <span className="file-icon">{entry.isDir ? '▣' : '▤'}</span>
                    <span className="file-name">{entry.name}</span>
                    <span className="file-size nowrap">{entry.isDir ? '文件夹' : formatBytes(entry.size)}</span>
                    <span className="small faint nowrap" style={{ width: 128, textAlign: 'right' }}>
                      {formatTime(entry.modTime)}
                    </span>
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}

      {tab === 'cloud' &&
        (!connected ? (
          <div className="card">
            <Empty
              icon="☁"
              title="尚未连接同步服务器"
              hint="请先在「同步服务器」页面登录并连接，然后即可在云端存取文件"
            />
          </div>
        ) : (
          <>
            <div className="row" style={{ gap: 14, marginBottom: 12, alignItems: 'stretch' }}>
              <div style={{ width: 240 }}>
                <Stat
                  label="云端用量"
                  value={usage ? formatBytes(usage.usedBytes) : '—'}
                  sub={usage ? `配额 ${formatBytes(usage.quotaBytes)}` : '正在读取用量'}
                />
              </div>
              <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column', justifyContent: 'center', gap: 8 }}>
                <div className="row small faint">
                  <span>已使用 {usagePct.toFixed(1)}%</span>
                  <div className="spacer" />
                  <span>
                    {usage ? `${formatBytes(usage.usedBytes)} / ${formatBytes(usage.quotaBytes)} · 剩余 ${formatBytes(Math.max(0, usage.quotaBytes - usage.usedBytes))}` : '—'}
                  </span>
                </div>
                <Progress percent={usagePct} />
              </div>
            </div>

            <div className="card">
              <div className="row row-wrap">
                <div className="crumbs" style={{ flex: 1, minWidth: 200 }}>
                  {cloudCrumbs.map((c) => (
                    <span key={c.path} className="row" style={{ gap: 2 }}>
                      <button className="crumb" onClick={() => void loadCloud(c.path)} title={c.path}>
                        {c.label}
                      </button>
                      <span className="faint">›</span>
                    </span>
                  ))}
                </div>
                <button className="btn btn-sm" onClick={() => void loadCloud(cloudPath)} disabled={cloudBusy}>
                  刷新
                </button>
                <button className="btn btn-sm" onClick={() => setMkdirOpen(true)}>
                  新建文件夹
                </button>
                <button className="btn btn-sm" onClick={uploadLocalFile}>
                  上传本地文件
                </button>
                <button className="btn btn-sm" disabled={!cloudEntry || cloudEntry.isDir} onClick={downloadCloud}>
                  下载到本地
                </button>
                <button
                  className="btn btn-sm"
                  disabled={!cloudEntry}
                  onClick={() => {
                    if (!cloudEntry) return
                    setRenameName(cloudEntry.name)
                    setRenameOpen(true)
                  }}
                >
                  重命名
                </button>
                <button className="btn btn-danger btn-sm" disabled={!cloudEntry} onClick={() => setRemoving(cloudEntry)}>
                  删除
                </button>
              </div>

              <div className="divider" />

              {cloudBusy ? (
                <div style={{ padding: 20 }}>
                  <Spinner label="读取云端目录" />
                </div>
              ) : cloudEntries.length === 0 ? (
                <Empty icon="☁" title="云端目录是空的" hint="可以把本地文件上传到这里" />
              ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                  {cloudEntries.map((entry) => (
                    <div
                      key={entry.path}
                      className={`file-row ${cloudSelected === entry.path ? 'selected' : ''}`}
                      onClick={() => setCloudSelected(entry.path)}
                      onDoubleClick={() => entry.isDir && void loadCloud(entry.path)}
                      title={entry.path}
                    >
                      <span className="file-icon">{entry.isDir ? '▣' : '▤'}</span>
                      <span className="file-name">{entry.name}</span>
                      <span className="file-size nowrap">{entry.isDir ? '文件夹' : formatBytes(entry.size)}</span>
                      <span className="small faint nowrap" style={{ width: 128, textAlign: 'right' }}>
                        {formatTime(entry.modTime)}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        ))}

      {tab === 'transfers' && (
        <div className="card">
          <div className="row">
            <h3 className="card-title">传输记录</h3>
            <div className="spacer" />
            {transfers.length > 0 ? (
              <button className="btn btn-sm btn-ghost" onClick={() => setTransfers((prev) => prev.filter((t) => !t.finished))}>
                清除已完成
              </button>
            ) : null}
          </div>
          <p className="card-desc">实时展示云端上传下载、SFTP 与 FTP 的传输进度。</p>

          {transfers.length === 0 ? (
            <Empty icon="⇅" title="还没有传输记录" hint="上传或下载文件后，这里会显示实时进度" />
          ) : (
            <div className="list">
              {transfers.map((t) => {
                const pct = t.total > 0 ? percent(t.done, t.total) : t.finished ? 100 : 0
                return (
                  <div
                    key={t.key}
                    className="list-row"
                    style={{ flexDirection: 'column', alignItems: 'stretch', gap: 8 }}
                  >
                    <div className="row">
                      <span className="file-icon">{isUploadKind(t.kind) ? '↑' : '↓'}</span>
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div className="name truncate">{basename(t.path) || t.key}</div>
                        <div className="meta truncate mono" title={t.path}>
                          {transferLabel(t.kind)} · {t.path || '—'}
                        </div>
                      </div>
                      {t.finished ? <Tag kind="ok">已完成</Tag> : <Tag kind="info">传输中</Tag>}
                      <span className="small faint mono nowrap">
                        {formatBytes(t.done)} / {formatBytes(t.total)}
                      </span>
                      <span className="small mono nowrap" style={{ width: 84, textAlign: 'right' }}>
                        {t.finished ? '—' : formatSpeed(t.speedBps)}
                      </span>
                    </div>
                    <Progress percent={pct} indeterminate={!t.finished && t.total <= 0} />
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}

      {mkdirOpen ? (
        <Modal
          title="新建文件夹"
          onClose={() => setMkdirOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setMkdirOpen(false)}>
                取消
              </button>
              <button className="btn btn-primary" onClick={submitMkdir}>
                创建
              </button>
            </>
          }
        >
          <Field label="文件夹名称" hint={`将创建在 ${cloudPath || '/'}`}>
            <input
              className="input"
              value={mkdirName}
              autoFocus
              onChange={(e) => setMkdirName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && void submitMkdir()}
            />
          </Field>
        </Modal>
      ) : null}

      {renameOpen ? (
        <Modal
          title="重命名"
          onClose={() => setRenameOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setRenameOpen(false)}>
                取消
              </button>
              <button className="btn btn-primary" onClick={submitRename}>
                保存
              </button>
            </>
          }
        >
          <Field label="新名称">
            <input
              className="input"
              value={renameName}
              autoFocus
              onChange={(e) => setRenameName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && void submitRename()}
            />
          </Field>
        </Modal>
      ) : null}

      {removing ? (
        <Confirm
          title="删除云端文件"
          message={`确定删除「${removing.name}」${removing.isDir ? '及其全部内容' : ''}吗？该操作不可撤销。`}
          danger
          onCancel={() => setRemoving(null)}
          onConfirm={submitRemove}
        />
      ) : null}
    </div>
  )
}

/* ---------------- helpers ---------------- */

function sortEntries<T extends { name: string; isDir: boolean }>(entries: T[]): T[] {
  return [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name, 'zh-Hans-CN')
  })
}

function serverJoin(dir: string, name: string): string {
  const base = dir.endsWith('/') ? dir.slice(0, -1) : dir
  return `${base}/${name}`
}

function buildCrumbs(path: string, separator: string): Array<{ label: string; path: string }> {
  if (!path) return []
  const sep = separator || (path.includes('\\') ? '\\' : '/')
  const parts = path.split(/[\\/]+/).filter(Boolean)
  const unixRoot = path.startsWith('/')
  const crumbs: Array<{ label: string; path: string }> = []
  let acc = ''
  parts.forEach((part, index) => {
    if (index === 0) {
      if (unixRoot) {
        acc = `/${part}`
      } else if (/^[A-Za-z]:$/.test(part)) {
        // Windows 盘符：补上分隔符，返回根目录而不是当前目录
        acc = `${part}${sep}`
      } else {
        acc = part
      }
    } else {
      acc = acc.endsWith(sep) ? `${acc}${part}` : `${acc}${sep}${part}`
    }
    crumbs.push({ label: part, path: acc })
  })
  return crumbs
}

function buildCloudCrumbs(path: string): Array<{ label: string; path: string }> {
  const crumbs: Array<{ label: string; path: string }> = [{ label: '云端根目录', path: '/' }]
  const parts = path.split('/').filter(Boolean)
  let acc = ''
  for (const part of parts) {
    acc += `/${part}`
    crumbs.push({ label: part, path: acc })
  }
  return crumbs
}

function mergeTransfer(list: TransferItem[], patch: Partial<TransferItem> & { key: string }): TransferItem[] {
  const index = list.findIndex((t) => t.key === patch.key)
  if (index === -1) {
    const blank: TransferItem = {
      key: patch.key,
      kind: '',
      path: '',
      done: 0,
      total: 0,
      finished: false,
      bytes: 0,
      speedBps: 0,
    }
    return [{ ...blank, ...patch }, ...list]
  }
  const next = [...list]
  // 已完成的传输不会被后续迟到的进度事件降级
  next[index] = { ...next[index], ...patch, finished: next[index].finished || (patch.finished ?? false) }
  return next
}

function isUploadKind(kind: string): boolean {
  return kind.endsWith('upload')
}

function transferLabel(kind: string): string {
  switch (kind) {
    case 'cloud-upload':
      return '上传到云端'
    case 'cloud-download':
      return '从云端下载'
    case 'sftp-upload':
      return 'SFTP 上传'
    case 'sftp-download':
      return 'SFTP 下载'
    case 'ftp-upload':
      return 'FTP 上传'
    case 'ftp-download':
      return 'FTP 下载'
    case 'netdisk-upload':
      return '网盘上传'
    default:
      return kind || '传输'
  }
}
