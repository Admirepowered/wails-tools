import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, onEvent, type DownloadRequest, type DownloadTask } from '../lib/api'
import { Empty, Progress, Spinner, Tag, useToast } from '../components/ui'
import { formatBytes, formatSpeed, formatTime, percent } from '../lib/format'
import type { PageProps } from '../App'

type Filter = 'all' | 'active' | 'completed'

export default function DownloadsPage({ settings }: PageProps) {
  const toast = useToast()
  const [tasks, setTasks] = useState<DownloadTask[]>([])
  const [url, setUrl] = useState('')
  const [filename, setFilename] = useState('')
  const [directory, setDirectory] = useState(settings?.downloads?.directory ?? '')
  const [connections, setConnections] = useState(settings?.downloads?.connectionsPerTask || 8)
  const [proxyMode, setProxyMode] = useState<'inherit' | 'off' | 'on'>('inherit')
  const [probe, setProbe] = useState<{ ok: boolean; filename?: string; total?: number; error?: string } | null>(null)
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState<Filter>('all')
  const dirTouched = useRef(false)

  const reload = useCallback(async () => {
    try {
      setTasks(await api.ListDownloads())
    } catch (err) {
      toast.error(err)
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    void reload()
  }, [reload])

  useEffect(() => {
    if (!dirTouched.current && settings?.downloads?.directory) {
      setDirectory(settings.downloads.directory)
    }
    if (settings?.downloads?.connectionsPerTask) {
      setConnections(settings.downloads.connectionsPerTask)
    }
  }, [settings])

  useEffect(() => {
    const off = onEvent<{ kind: string; task?: DownloadTask }>('download:event', (ev) => {
      if (ev?.kind === 'task' && ev.task) {
        const task = ev.task
        setTasks((prev) => {
          const idx = prev.findIndex((t) => t.id === task.id)
          if (idx === -1) return [...prev, task]
          const next = [...prev]
          next[idx] = task
          return next
        })
      }
      if (ev?.kind === 'done' && ev.task) {
        toast.push(`下载完成：${ev.task.filename}`)
      }
    })
    return () => off()
  }, [toast])

  const add = async (start: boolean) => {
    if (!url.trim()) {
      toast.push('请输入下载地址', 'warn')
      return
    }
    const req: DownloadRequest = {
      url: url.trim(),
      filename: filename.trim() || undefined,
      directory: directory || undefined,
      connections,
      proxyMode,
    }
    try {
      const task = start ? await api.AddDownloadAndStart(req) : await api.AddDownload(req)
      setTasks((prev) => [...prev.filter((t) => t.id !== task.id), task])
      setUrl('')
      setFilename('')
      setProbe(null)
    } catch (err) {
      toast.error(err)
    }
  }

  const runProbe = async () => {
    if (!url.trim()) return
    try {
      const res = await api.ProbeURL(url.trim())
      setProbe(res)
      if (res.ok && res.filename && !filename) setFilename(res.filename)
      if (!res.ok) toast.push(res.error || '探测失败', 'warn')
    } catch (err) {
      toast.error(err)
    }
  }

  const act = async (fn: () => Promise<void>) => {
    try {
      await fn()
      await reload()
    } catch (err) {
      toast.error(err)
    }
  }

  const visible = useMemo(() => {
    const list = filter === 'all' ? tasks : filter === 'active' ? tasks.filter((t) => ['queued', 'preparing', 'downloading', 'paused'].includes(t.status)) : tasks.filter((t) => ['completed', 'failed', 'canceled'].includes(t.status))
    return [...list].reverse()
  }, [tasks, filter])

  const stats = useMemo(() => {
    const active = tasks.filter((t) => t.status === 'downloading')
    return {
      active: active.length,
      speed: active.reduce((sum, t) => sum + (t.speedBps || 0), 0),
      total: tasks.length,
    }
  }, [tasks])

  return (
    <div>
      <div className="card">
        <h3 className="card-title">新建下载</h3>
        <p className="card-desc">支持 HTTP/HTTPS，多连接分段下载、断点续传，可按需走代理。</p>
        <div className="row" style={{ gap: 8 }}>
          <input
            className="input mono"
            style={{ flex: 1 }}
            placeholder="https://example.com/file.zip"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && add(true)}
          />
          <button className="btn" onClick={runProbe}>
            探测
          </button>
          <button className="btn" onClick={() => add(false)}>
            加入队列
          </button>
          <button className="btn btn-primary" onClick={() => add(true)}>
            开始下载
          </button>
        </div>

        {probe && (
          <div className="row small" style={{ marginTop: 8, gap: 10 }}>
            {probe.ok ? (
              <>
                <Tag kind="ok">可下载</Tag>
                <span className="dim mono">{probe.filename}</span>
                <span className="faint">{formatBytes(probe.total)}</span>
              </>
            ) : (
              <Tag kind="bad">{probe.error}</Tag>
            )}
          </div>
        )}

        <div className="divider" />
        <div className="grid-4">
          <div className="field">
            <label>保存文件名（可选）</label>
            <input className="input" placeholder="自动识别" value={filename} onChange={(e) => setFilename(e.target.value)} />
          </div>
          <div className="field">
            <label>保存目录</label>
            <div className="row" style={{ gap: 6 }}>
              <input
                className="input mono truncate"
                value={directory}
                onChange={(e) => {
                  dirTouched.current = true
                  setDirectory(e.target.value)
                }}
              />
              <button
                className="btn btn-sm"
                onClick={async () => {
                  try {
                    const dir = await api.PickDirectory('选择保存目录')
                    if (dir) {
                      dirTouched.current = true
                      setDirectory(dir)
                    }
                  } catch (err) {
                    toast.error(err)
                  }
                }}
              >
                …
              </button>
            </div>
          </div>
          <div className="field">
            <label>连接数</label>
            <input
              className="input"
              type="number"
              min={1}
              max={32}
              value={connections}
              onChange={(e) => setConnections(Math.max(1, Math.min(32, Number(e.target.value) || 1)))}
            />
          </div>
          <div className="field">
            <label>代理</label>
            <select className="select" value={proxyMode} onChange={(e) => setProxyMode(e.target.value as typeof proxyMode)}>
              <option value="inherit">跟随代理设置</option>
              <option value="off">本次直连</option>
              <option value="on">强制走代理</option>
            </select>
          </div>
        </div>
      </div>

      <div className="row" style={{ margin: '16px 0 10px' }}>
        <div className="tabs" style={{ flex: 1, marginBottom: 0, borderBottom: 'none' }}>
          {(
            [
              ['all', `全部 ${tasks.length}`],
              ['active', `进行中 ${stats.active}`],
              ['completed', '已结束'],
            ] as Array<[Filter, string]>
          ).map(([id, label]) => (
            <button key={id} className={`tab ${filter === id ? 'active' : ''}`} onClick={() => setFilter(id)}>
              {label}
            </button>
          ))}
        </div>
        <span className="small faint nowrap">总速度 {formatSpeed(stats.speed)}</span>
        <button className="btn btn-sm" onClick={() => act(async () => api.PauseAllDownloads())}>
          全部暂停
        </button>
        <button className="btn btn-sm" onClick={() => act(async () => api.ClearFinishedDownloads())}>
          清除已完成
        </button>
      </div>

      {loading ? (
        <div style={{ padding: 24 }}>
          <Spinner label="加载下载列表" />
        </div>
      ) : visible.length === 0 ? (
        <Empty icon="↓" title="还没有下载任务" hint="在上方粘贴链接即可开始" />
      ) : (
        <div className="list">
          {visible.map((t) => (
            <TaskRow key={t.id} task={t} onAction={act} />
          ))}
        </div>
      )}
    </div>
  )
}

function statusTag(t: DownloadTask) {
  switch (t.status) {
    case 'downloading':
      return <Tag kind="ok">下载中</Tag>
    case 'preparing':
      return <Tag kind="info">准备中</Tag>
    case 'queued':
      return <Tag>排队中</Tag>
    case 'paused':
      return <Tag kind="warn">已暂停</Tag>
    case 'completed':
      return <Tag kind="ok">已完成</Tag>
    case 'failed':
      return <Tag kind="bad">失败</Tag>
    case 'canceled':
      return <Tag kind="bad">已取消</Tag>
    default:
      return <Tag>{t.status}</Tag>
  }
}

function TaskRow({ task, onAction }: { task: DownloadTask; onAction: (fn: () => Promise<void>) => Promise<void> }) {
  const toast = useToast()
  const pct = task.total > 0 ? percent(task.downloaded, task.total) : task.percent
  const running = task.status === 'downloading' || task.status === 'preparing'

  return (
    <div className="list-row" style={{ alignItems: 'stretch', flexDirection: 'column', gap: 8 }}>
      <div className="row">
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="name truncate" title={task.filename}>
            {task.filename}
          </div>
          <div className="meta truncate mono" title={task.url}>
            {task.url}
          </div>
        </div>
        {statusTag(task)}
        <div className="small faint mono nowrap" style={{ width: 130, textAlign: 'right' }}>
          {formatBytes(task.downloaded)} / {formatBytes(task.total)}
        </div>
        <div className="small mono nowrap" style={{ width: 92, textAlign: 'right' }}>
          {formatSpeed(task.speedBps)}
        </div>
      </div>

      <div className="row" style={{ gap: 10 }}>
        <div style={{ flex: 1 }}>
          <Progress percent={pct} indeterminate={task.status === 'preparing'} />
        </div>
        <span className="small faint mono nowrap" style={{ width: 52, textAlign: 'right' }}>
          {task.total > 0 ? `${pct.toFixed(1)}%` : '—'}
        </span>
      </div>

      <div className="row">
        <span className="small faint">
          {task.connections} 连接 · {task.proxyUsed || '直连'} · {formatTime(task.updatedAt)}
          {task.retries > 0 ? ` · 重试 ${task.retries}` : ''}
        </span>
        {task.error ? <span className="small" style={{ color: 'var(--danger)' }}>{task.error}</span> : null}
        <div className="spacer" />
        {running ? (
          <button className="btn btn-sm" onClick={() => onAction(() => api.PauseDownload(task.id))}>
            暂停
          </button>
        ) : (
          <button
            className="btn btn-sm"
            disabled={task.status === 'completed'}
            onClick={() => onAction(() => api.StartDownload(task.id))}
          >
            {task.status === 'paused' ? '继续' : '开始'}
          </button>
        )}
        <button
          className="btn btn-sm"
          onClick={() => onAction(() => api.CancelDownload(task.id))}
          disabled={task.status === 'completed' || task.status === 'canceled'}
        >
          取消
        </button>
        <button
          className="btn btn-sm"
          onClick={() =>
            onAction(async () => {
              await api.OpenPath(task.directory)
            })
          }
        >
          打开目录
        </button>
        <button
          className="btn btn-danger btn-sm"
          onClick={() =>
            onAction(async () => {
              await api.RemoveDownload(task.id, false)
              toast.push('记录已移除')
            })
          }
        >
          移除
        </button>
      </div>
    </div>
  )
}
