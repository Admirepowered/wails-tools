import { useCallback, useEffect, useState } from 'react'
import { api, onEvent, type UpdateRelease, type UpdateSettings, type UpdateStatus } from '../lib/api'
import { Empty, Field, Progress, Spinner, Switch, Tag, useToast } from '../components/ui'
import { formatBytes, formatTime } from '../lib/format'
import type { PageProps } from '../App'

export default function UpdatePage({ settings, reloadSettings }: PageProps) {
  const toast = useToast()
  const [current, setCurrent] = useState('')
  const [cfg, setCfg] = useState<UpdateSettings | null>(null)
  const [status, setStatus] = useState<UpdateStatus | null>(null)
  const [releases, setReleases] = useState<UpdateRelease[]>([])
  const [checking, setChecking] = useState(false)
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null)
  const [staged, setStaged] = useState('')
  const [probing, setProbing] = useState(false)
  const [probe, setProbe] = useState<Record<string, any> | null>(null)
  const [notes, setNotes] = useState('')
  const [uploading, setUploading] = useState(false)

  const load = useCallback(async () => {
    try {
      const [version, updateCfg, list] = await Promise.all([
        api.UpdateCurrentVersion(),
        api.GetUpdateSettings(),
        api.ListServerReleases('', '').catch(() => [] as UpdateRelease[]),
      ])
      setCurrent(version)
      setCfg(updateCfg)
      setReleases(list)
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    const off = onEvent<{ key?: string; kind?: string; done?: number; total?: number }>('transfer:progress', (ev) => {
      if (ev?.kind === 'update' || ev?.kind === 'update-upload') {
        setProgress({ done: ev.done ?? 0, total: ev.total ?? 0 })
      }
    })
    return () => off()
  }, [])

  const check = async () => {
    setChecking(true)
    setStaged('')
    try {
      const res = await api.CheckUpdate()
      setStatus(res)
      if (res.available) {
        toast.push(`发现新版本 ${res.latest}（${res.source === 'github' ? 'GitHub' : '服务器'}）`)
      } else {
        toast.push(res.message || '已是最新版本')
      }
    } catch (err) {
      toast.error(err)
    } finally {
      setChecking(false)
    }
  }

  const downloadAndApply = async () => {
    if (!status?.release) return
    try {
      setProgress({ done: 0, total: status.release.size })
      const path = await api.DownloadUpdate(status.release)
      setStaged(path)
      toast.push('下载完成，准备安装')
      await api.ApplyUpdate(path)
      toast.push('正在重启以完成更新…')
    } catch (err) {
      toast.error(err)
    } finally {
      setProgress(null)
    }
  }

  const saveCfg = async (next: UpdateSettings) => {
    try {
      const saved = await api.SaveUpdateSettings(next)
      setCfg(saved)
      toast.push('更新设置已保存')
    } catch (err) {
      toast.error(err)
    }
  }

  const probeGitHub = async () => {
    setProbing(true)
    try {
      setProbe(await api.ProbeGitHub())
    } catch (err) {
      toast.error(err)
    } finally {
      setProbing(false)
    }
  }

  const upload = async () => {
    setUploading(true)
    try {
      await api.UploadCurrentBuild(cfg?.channel || 'stable', notes, false)
      toast.push('当前版本已上传到同步服务器')
      setNotes('')
      await load()
    } catch (err) {
      toast.error(err)
    } finally {
      setUploading(false)
      setProgress(null)
    }
  }

  return (
    <div>
      <div className="card">
        <div className="row">
          <div>
            <h3 className="card-title">自动更新</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              当前版本 <span className="mono">{current || '…'}</span>
              {settings?.server?.host ? ` · 同步服务器 ${settings.server.host}:${settings.server.port}` : ''}
            </p>
          </div>
          <div className="spacer" />
          <button className="btn" onClick={probeGitHub} disabled={probing}>
            {probing ? '测试中…' : 'GitHub 连通性'}
          </button>
          <button className="btn btn-primary" onClick={check} disabled={checking}>
            {checking ? '检查中…' : '检查更新'}
          </button>
        </div>

        {probe && (
          <div className="row small" style={{ marginTop: 10, gap: 10 }}>
            <Tag kind={probe.ok ? 'ok' : 'bad'}>{probe.ok ? 'GitHub 可达' : 'GitHub 不可达'}</Tag>
            <span className="faint mono">{String(probe.strategy ?? '')}</span>
            <span className="faint">{String(probe.latencyMs ?? 0)} ms</span>
            {probe.error ? <span style={{ color: 'var(--danger)' }}>{String(probe.error)}</span> : null}
          </div>
        )}

        {status && (
          <div className="col" style={{ marginTop: 12, gap: 8 }}>
            <div className="row">
              <Tag kind={status.available ? 'warn' : 'ok'}>
                {status.available ? `可更新到 ${status.latest}` : '已是最新'}
              </Tag>
              <span className="small faint">
                {status.message} · {status.source === 'github' ? 'GitHub' : '同步服务器'}
                {status.transport ? ` · ${status.transport}` : ''}
              </span>
              <div className="spacer" />
              {status.checkedAt ? <span className="small faint">{formatTime(status.checkedAt)}</span> : null}
            </div>
            <div className="row small faint mono" style={{ gap: 12 }}>
              {status.currentHash ? <span title={status.currentHash}>本地 {status.currentHash.slice(0, 12)}…</span> : null}
              {status.releaseHash ? <span title={status.releaseHash}>远端 {status.releaseHash.slice(0, 12)}…</span> : null}
              {status.sameBuild ? <Tag kind="ok">哈希一致</Tag> : <Tag kind="warn">哈希不同</Tag>}
            </div>
            {status.release && (
              <div className="row small faint">
                <span className="mono">{status.release.filename}</span>
                <span>{formatBytes(status.release.size)}</span>
                {status.release.sha256 ? <span className="truncate">sha256 {status.release.sha256.slice(0, 16)}…</span> : null}
              </div>
            )}
            {status.error ? <div className="small" style={{ color: 'var(--danger)' }}>{status.error}</div> : null}
            {progress && (
              <div className="col" style={{ gap: 4 }}>
                <Progress percent={progress.total > 0 ? (progress.done / progress.total) * 100 : 0} />
                <div className="small faint">
                  {formatBytes(progress.done)} / {progress.total > 0 ? formatBytes(progress.total) : '—'}
                </div>
              </div>
            )}
            {status.available && (
              <div className="row">
                <button className="btn btn-primary" onClick={downloadAndApply} disabled={!!progress}>
                  下载并安装
                </button>
                {staged ? <span className="small faint truncate mono">{staged}</span> : null}
              </div>
            )}
          </div>
        )}
      </div>

      {cfg && (
        <div className="card">
          <h3 className="card-title">更新源</h3>
          <p className="card-desc">优先从同步服务器拉取（各客户端上传的构建），失败或未配置时回退到 GitHub Release。</p>
          <div className="grid-2">
            <Field label="更新源">
              <select
                className="select"
                value={cfg.source}
                onChange={(e) => saveCfg({ ...cfg, source: e.target.value })}
              >
                <option value="auto">自动（服务器 → GitHub）</option>
                <option value="server">仅同步服务器</option>
                <option value="github">仅 GitHub</option>
              </select>
            </Field>
            <Field label="更新通道">
              <select
                className="select"
                value={cfg.channel}
                onChange={(e) => saveCfg({ ...cfg, channel: e.target.value })}
              >
                <option value="stable">stable</option>
                <option value="beta">beta</option>
              </select>
            </Field>
          </div>
          <div className="grid-2" style={{ marginTop: 10 }}>
            <Field label="GitHub 仓库">
              <input
                className="input mono"
                value={cfg.githubRepo}
                onChange={(e) => setCfg({ ...cfg, githubRepo: e.target.value })}
                onBlur={() => saveCfg(cfg)}
              />
            </Field>
            <div className="col" style={{ gap: 8 }}>
              <label className="row" style={{ gap: 8 }}>
                <Switch
                  checked={cfg.githubBypass}
                  onChange={(v) => saveCfg({ ...cfg, githubBypass: v })}
                />
                <span className="small">绕过 SNI 检测（固定 IP + Host 复用）</span>
              </label>
              <label className="row" style={{ gap: 8 }}>
                <Switch
                  checked={cfg.githubFragment}
                  onChange={(v) => saveCfg({ ...cfg, githubFragment: v })}
                />
                <span className="small">TLS ClientHello 分片（对抗 SNI DPI）</span>
              </label>
              <label className="row" style={{ gap: 8 }}>
                <Switch checked={cfg.autoCheck} onChange={(v) => saveCfg({ ...cfg, autoCheck: v })} />
                <span className="small">启动时自动检查</span>
              </label>
            </div>
          </div>
        </div>
      )}

      <div className="card">
        <h3 className="card-title">发布构建（管理员）</h3>
        <p className="card-desc">
          把当前运行的客户端上传到同步服务器，其他客户端即可从服务器自动更新。需要管理员账号。
        </p>
        <div className="row" style={{ gap: 8 }}>
          <input
            className="input"
            style={{ flex: 1 }}
            placeholder="版本说明（可选）"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
          />
          <button className="btn btn-primary" onClick={upload} disabled={uploading}>
            {uploading ? '上传中…' : '上传当前版本'}
          </button>
        </div>
        {progress && (
          <div style={{ marginTop: 10 }}>
            <Progress percent={progress.total > 0 ? (progress.done / progress.total) * 100 : 0} />
          </div>
        )}

        <div className="divider" />
        {releases.length === 0 ? (
          <Empty icon="⟳" title="服务器上还没有构建" hint="上传当前版本后即可在这里看到" />
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>版本</th>
                <th>平台</th>
                <th>通道</th>
                <th>文件</th>
                <th className="right">大小</th>
                <th className="right">下载</th>
                <th className="right">操作</th>
              </tr>
            </thead>
            <tbody>
              {releases.map((r) => (
                <tr key={r.id}>
                  <td className="mono">{r.version}</td>
                  <td>
                    {r.platform}/{r.arch}
                  </td>
                  <td>
                    <Tag kind={r.channel === 'beta' ? 'warn' : 'default'}>{r.channel}</Tag>
                  </td>
                  <td className="truncate mono small" title={r.filename}>
                    {r.filename}
                  </td>
                  <td className="right mono small">{formatBytes(r.size)}</td>
                  <td className="right mono small">{r.downloads ?? 0}</td>
                  <td className="right">
                    <button
                      className="btn btn-danger btn-sm"
                      onClick={async () => {
                        try {
                          await api.DeleteServerRelease(r.id)
                          await load()
                          toast.push('已删除构建')
                        } catch (err) {
                          toast.error(err)
                        }
                      }}
                    >
                      删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}

