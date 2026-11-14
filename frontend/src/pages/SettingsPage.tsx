import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type AppInfo, type AutostartStatus, type DownloadSettings, type Settings, type StartupSettings } from '../lib/api'
import { useDebouncedValue } from '../lib/autosave'
import { Field, Spinner, Switch, Tag, useToast } from '../components/ui'
import { formatBytes } from '../lib/format'
import type { PageProps } from '../App'

const MB = 1048576

interface DownloadForm {
  directory: string
  maxConcurrency: number
  connectionsPerTask: number
  chunkSizeMb: number
  speedLimitKBps: number
  retryCount: number
  userAgent: string
  autoResume: boolean
  verifySupported: boolean
  verifyChecksums: boolean
}

interface UiForm {
  theme: string
  language: string
  density: string
}

function toDownloadForm(settings: Settings): DownloadForm {
  const dl = settings.downloads
  const raw = dl as DownloadSettings & { verifyChecksums?: boolean }
  return {
    directory: dl.directory ?? '',
    maxConcurrency: dl.maxConcurrency || 3,
    connectionsPerTask: dl.connectionsPerTask || 8,
    chunkSizeMb: dl.chunkSizeBytes ? Math.max(1, Math.round((dl.chunkSizeBytes / MB) * 10) / 10) : 4,
    speedLimitKBps: dl.speedLimitKBps || 0,
    retryCount: typeof dl.retryCount === 'number' ? dl.retryCount : 3,
    userAgent: dl.userAgent ?? '',
    autoResume: dl.autoResume !== false,
    verifySupported: typeof raw.verifyChecksums === 'boolean',
    verifyChecksums: raw.verifyChecksums === true,
  }
}

function toUiForm(settings: Settings): UiForm {
  return {
    theme: settings.ui?.theme || 'dark',
    language: settings.ui?.language || 'zh-CN',
    density: settings.ui?.density || 'comfortable',
  }
}

function clamp(value: number, min: number, max: number): number {
  if (!Number.isFinite(value)) return min
  return Math.max(min, Math.min(max, value))
}

export default function SettingsPage(props: PageProps) {
  if (!props.settings) {
    return (
      <div className="card">
        <Spinner label="正在读取设置…" />
      </div>
    )
  }
  return <SettingsBody settings={props.settings} reloadSettings={props.reloadSettings} />
}

function SettingsBody({ settings, reloadSettings }: { settings: Settings; reloadSettings: () => Promise<void> }) {
  const toast = useToast()
  const [downloads, setDownloads] = useState<DownloadForm>(() => toDownloadForm(settings))
  const [uiForm, setUiForm] = useState<UiForm>(() => toUiForm(settings))
  const [autostart, setAutostart] = useState<AutostartStatus | null>(null)
  const [startup, setStartup] = useState<StartupSettings | null>(null)
  const [info, setInfo] = useState<AppInfo | null>(null)
  const [savingDl, setSavingDl] = useState(false)
  const [savingUi, setSavingUi] = useState(false)

  useEffect(() => {
    setDownloads(toDownloadForm(settings))
    setUiForm(toUiForm(settings))
    setStartup(settings.startup ?? null)
  }, [settings])

  useEffect(() => {
    let alive = true
    api
      .AppInfo()
      .then((res) => {
        if (alive) setInfo(res)
      })
      .catch((err) => toast.error(err))
    api
      .GetAutostartStatus()
      .then((res) => {
        if (alive) setAutostart(res)
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [toast])

  const patchDownloads = (patch: Partial<DownloadForm>) => {
    setDownloads((prev) => ({ ...prev, ...patch }))
  }

  // Seamless save: download defaults persist automatically.
  const debouncedDownloads = useDebouncedValue(downloads, 800)
  const downloadsFirstRun = useRef(true)
  useEffect(() => {
    if (downloadsFirstRun.current) {
      downloadsFirstRun.current = false
      return
    }
    void saveDownloads(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debouncedDownloads])

  const pickDirectory = async () => {
    try {
      const dir = await api.PickDirectory('选择默认下载目录')
      if (dir) patchDownloads({ directory: dir })
    } catch (err) {
      toast.error(err)
    }
  }

  const saveDownloads = async (silent = false) => {
    if (!silent) setSavingDl(true)
    try {
      const merged: DownloadSettings & { verifyChecksums?: boolean } = {
        ...settings.downloads,
        directory: downloads.directory.trim(),
        maxConcurrency: clamp(Math.round(downloads.maxConcurrency), 1, 10),
        connectionsPerTask: clamp(Math.round(downloads.connectionsPerTask), 1, 32),
        chunkSizeBytes: Math.max(MB, Math.round(downloads.chunkSizeMb * MB)),
        speedLimitKBps: Math.max(0, Math.round(downloads.speedLimitKBps)),
        retryCount: clamp(Math.round(downloads.retryCount), 0, 20),
        userAgent: downloads.userAgent.trim(),
        autoResume: downloads.autoResume,
      }
      if (downloads.verifySupported) merged.verifyChecksums = downloads.verifyChecksums
      await api.SaveSettings({ ...settings, downloads: merged })
      await reloadSettings()
      if (!silent) toast.push('下载设置已保存')
    } catch (err) {
      toast.error(err)
    } finally {
      if (!silent) setSavingDl(false)
    }
  }

  const saveUi = useCallback(
    async (next: UiForm) => {
      setUiForm(next)
      if (next.theme) document.documentElement.dataset.theme = next.theme
      setSavingUi(true)
      try {
        await api.SaveSettings({ ...settings, ui: { ...settings.ui, ...next } })
        await reloadSettings()
        toast.push('外观设置已保存')
      } catch (err) {
        toast.error(err)
      } finally {
        setSavingUi(false)
      }
    },
    [settings, reloadSettings, toast],
  )

  const openPath = async (target: string) => {
    if (!target) {
      toast.push('尚未设置目录', 'warn')
      return
    }
    try {
      await api.OpenPath(target)
    } catch (err) {
      toast.error(err)
    }
  }

  const downloadDir = downloads.directory || settings.downloads.directory || ''
  const server = settings.server
  const transport = (server.lastUsedTransport ?? '').toLowerCase()

  return (
    <div className="page-narrow">
      <div className="card">
        <div className="row">
          <div style={{ flex: 1 }}>
            <h3 className="card-title">下载设置</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              这些默认值会应用到新建的下载任务与网盘转存任务。
            </p>
          </div>
          <span className="small faint">修改后自动保存</span>
        </div>

        <div className="divider" />

        <Field label="默认下载目录" hint="留空则使用系统下载目录">
          <div className="row" style={{ gap: 6 }}>
            <input
              className="input mono truncate"
              value={downloads.directory}
              onChange={(e) => patchDownloads({ directory: e.target.value })}
              placeholder="例如 D:\\Downloads"
            />
            <button className="btn btn-sm" onClick={() => void pickDirectory()}>
              选择…
            </button>
            <button className="btn btn-sm" onClick={() => void openPath(downloadDir)}>
              打开
            </button>
          </div>
        </Field>

        <div className="grid-4" style={{ marginTop: 14 }}>
          <Field label="最大并发任务" hint="1 - 10">
            <input
              className="input"
              type="number"
              min={1}
              max={10}
              value={downloads.maxConcurrency}
              onChange={(e) => patchDownloads({ maxConcurrency: clamp(Number(e.target.value) || 1, 1, 10) })}
            />
          </Field>
          <Field label="单任务连接数" hint="1 - 32">
            <input
              className="input"
              type="number"
              min={1}
              max={32}
              value={downloads.connectionsPerTask}
              onChange={(e) => patchDownloads({ connectionsPerTask: clamp(Number(e.target.value) || 1, 1, 32) })}
            />
          </Field>
          <Field label="分片大小（MB）" hint="按 MB 填写，保存时换算为字节">
            <input
              className="input"
              type="number"
              min={1}
              max={64}
              step={0.5}
              value={downloads.chunkSizeMb}
              onChange={(e) => patchDownloads({ chunkSizeMb: clamp(Number(e.target.value) || 1, 1, 64) })}
            />
          </Field>
          <Field label="限速（KB/s）" hint="0 表示不限速">
            <input
              className="input"
              type="number"
              min={0}
              value={downloads.speedLimitKBps}
              onChange={(e) => patchDownloads({ speedLimitKBps: Math.max(0, Number(e.target.value) || 0) })}
            />
          </Field>
          <Field label="失败重试次数" hint="0 - 20">
            <input
              className="input"
              type="number"
              min={0}
              max={20}
              value={downloads.retryCount}
              onChange={(e) => patchDownloads({ retryCount: clamp(Number(e.target.value) || 0, 0, 20) })}
            />
          </Field>
          <div style={{ gridColumn: 'span 3' }}>
            <Field label="User-Agent" hint="部分站点会校验 UA，留空使用内置默认值">
              <input
                className="input mono"
                value={downloads.userAgent}
                onChange={(e) => patchDownloads({ userAgent: e.target.value })}
                placeholder="Mozilla/5.0 ..."
              />
            </Field>
          </div>
        </div>

        <div className="divider" />

        <div className="row row-wrap">
          <div className="row" style={{ gap: 8 }}>
            <Switch checked={downloads.autoResume} onChange={(v) => patchDownloads({ autoResume: v })} />
            <span className="small dim">断点续传（中断后自动恢复）</span>
          </div>
          {downloads.verifySupported ? (
            <div className="row" style={{ gap: 8 }}>
              <Switch checked={downloads.verifyChecksums} onChange={(v) => patchDownloads({ verifyChecksums: v })} />
              <span className="small dim">下载完成后校验文件完整性</span>
            </div>
          ) : (
            <span className="small faint">当前后端未提供校验开关</span>
          )}
          <div className="spacer" />
          <span className="small faint">
            分片 {formatBytes(Math.round(downloads.chunkSizeMb * MB))}
            {downloads.speedLimitKBps > 0 ? ` · 限速 ${downloads.speedLimitKBps} KB/s` : ' · 不限速'}
          </span>
        </div>
      </div>

      <div className="card">
        <h3 className="card-title">外观</h3>
        <p className="card-desc">主题切换会立即生效，语言与密度在重新打开界面后完全生效。</p>
        <div className="grid-3">
          <Field label="主题" hint="深色更省眼，浅色更明亮">
            <select
              className="select"
              value={uiForm.theme}
              disabled={savingUi}
              onChange={(e) => void saveUi({ ...uiForm, theme: e.target.value })}
            >
              <option value="dark">深色（dark）</option>
              <option value="light">浅色（light）</option>
            </select>
          </Field>
          <Field label="信息密度" hint="紧凑模式会缩小行高">
            <select
              className="select"
              value={uiForm.density}
              disabled={savingUi}
              onChange={(e) => void saveUi({ ...uiForm, density: e.target.value })}
            >
              <option value="comfortable">舒适（comfortable）</option>
              <option value="compact">紧凑（compact）</option>
            </select>
          </Field>
          <Field label="界面语言" hint="默认简体中文">
            <select
              className="select"
              value={uiForm.language}
              disabled={savingUi}
              onChange={(e) => void saveUi({ ...uiForm, language: e.target.value })}
            >
              <option value="zh-CN">简体中文</option>
              <option value="en-US">English</option>
            </select>
          </Field>
        </div>
      </div>

      <div className="card">
        <h3 className="card-title">启动与后台</h3>
        <p className="card-desc">开机自启动会在当前用户登录时拉起 WailsTools，并立即开始同步与订阅刷新。</p>
        <div className="row row-wrap" style={{ gap: 16 }}>
          <label className="row" style={{ gap: 8 }}>
            <Switch
              checked={autostart?.enabled ?? false}
              onChange={async (v) => {
                try {
                  const st = await api.SetAutostart(v)
                  setAutostart(st)
                  toast.push(v ? '已设置为开机自启动' : '已关闭开机自启动')
                } catch (err) {
                  toast.error(err)
                }
              }}
            />
            <span className="small">开机自启动</span>
          </label>
          {autostart ? (
            autostart.supported ? (
              <Tag kind={autostart.enabled ? 'ok' : 'default'}>
                {autostart.enabled ? '已注册' : '未注册'}
              </Tag>
            ) : (
              <Tag kind="warn">当前平台不支持</Tag>
            )
          ) : null}
          <div className="spacer" />
          <button
            className="btn btn-sm"
            onClick={async () => {
              try {
                const [st, watch] = await Promise.all([api.GetAutostartStatus(), api.ClipboardWatching()])
                setAutostart(st)
                toast.push(`剪贴板监听：${watch ? '开启' : '关闭'}`)
              } catch (err) {
                toast.error(err)
              }
            }}
          >
            刷新状态
          </button>
        </div>
        <div className="divider" />
        <div className="row row-wrap" style={{ gap: 16 }}>
          <label className="row" style={{ gap: 8 }}>
            <Switch
              checked={startup?.trayEnabled ?? true}
              onChange={async (v) => {
                try {
                  const next = await api.SetTrayEnabled(v)
                  setStartup(next)
                  toast.push(v ? '已启用系统托盘' : '已关闭系统托盘')
                } catch (err) {
                  toast.error(err)
                }
              }}
            />
            <span className="small">系统托盘图标</span>
          </label>
          <label className="row" style={{ gap: 8 }}>
            <Switch
              checked={startup?.closeToTray ?? true}
              onChange={async (v) => {
                try {
                  const next = await api.SetCloseToTray(v)
                  setStartup(next)
                  toast.push(v ? '关闭窗口时最小化到托盘' : '关闭窗口时直接退出')
                } catch (err) {
                  toast.error(err)
                }
              }}
            />
            <span className="small">关闭窗口时最小化到托盘</span>
          </label>
          <label className="row" style={{ gap: 8 }}>
            <Switch
              checked={startup?.startMinimized ?? false}
              onChange={async (v) => {
                try {
                  const current = startup ?? { enabled: false, startMinimized: false, trayEnabled: true, closeToTray: true }
                  const saved = await api.SaveSettings({
                    ...settings,
                    startup: { ...current, startMinimized: v },
                  })
                  setStartup(saved.startup ?? null)
                  await reloadSettings()
                  toast.push(v ? '启动时最小化到托盘（下次启动生效）' : '启动时显示窗口')
                } catch (err) {
                  toast.error(err)
                }
              }}
            />
            <span className="small">启动时最小化到托盘</span>
          </label>
          <div className="spacer" />
          <button
            className="btn btn-sm"
            onClick={async () => {
              try {
                await api.HideMainWindow()
              } catch (err) {
                toast.error(err)
              }
            }}
          >
            最小化到托盘
          </button>
        </div>

        {autostart?.location ? (
          <div className="small faint mono truncate" style={{ marginTop: 8 }} title={autostart.location}>
            {autostart.location}
            {autostart.command ? ` → ${autostart.command}` : ''}
          </div>
        ) : null}
        {autostart?.error ? <div className="small" style={{ color: 'var(--danger)' }}>{autostart.error}</div> : null}
      </div>

      <div className="card">
        <h3 className="card-title">数据与关于</h3>
        <p className="card-desc">配置文件、笔记与下载记录都保存在数据目录中，可直接打开查看或备份。</p>

        {!info ? (
          <Spinner label="读取应用信息" />
        ) : (
          <>
            <table className="table">
              <tbody>
                <tr>
                  <td style={{ width: 140 }} className="faint">
                    版本
                  </td>
                  <td className="mono">{info.version || '—'}</td>
                </tr>
                <tr>
                  <td className="faint">平台</td>
                  <td className="mono">{info.platform || '—'}</td>
                </tr>
                <tr>
                  <td className="faint">Go 版本</td>
                  <td className="mono">{info.goVersion || '—'}</td>
                </tr>
                <tr>
                  <td className="faint">数据目录</td>
                  <td>
                    <div className="row" style={{ gap: 8 }}>
                      <span className="mono truncate" style={{ flex: 1 }} title={info.dataDir}>
                        {info.dataDir || '—'}
                      </span>
                      <button className="btn btn-sm" onClick={() => void api.OpenDataDir().catch((err) => toast.error(err))}>
                        打开
                      </button>
                    </div>
                  </td>
                </tr>
                <tr>
                  <td className="faint">笔记文件</td>
                  <td>
                    <div className="row" style={{ gap: 8 }}>
                      <span className="mono truncate" style={{ flex: 1 }} title={info.notesPath}>
                        {info.notesPath || '—'}
                      </span>
                      <button
                        className="btn btn-sm"
                        disabled={!info.notesPath}
                        onClick={() => void api.RevealInExplorer(info.notesPath).catch((err) => toast.error(err))}
                      >
                        定位
                      </button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>

            <div className="row" style={{ marginTop: 12 }}>
              <button className="btn btn-sm" onClick={() => void openPath(downloadDir)}>
                打开下载目录
              </button>
              <span className="small faint truncate" title={downloadDir}>
                {downloadDir || '未设置'}
              </span>
            </div>
          </>
        )}
      </div>

      <div className="card">
        <h3 className="card-title">服务器连接</h3>
        <p className="card-desc">同步服务器地址与传输偏好，可前往侧边栏「同步服务」页面修改与连接。</p>
        <div className="row row-wrap">
          <span className="mono">
            {server.host || '127.0.0.1'}:{server.port || 8642}
          </span>
          {transport === 'udp' ? <Tag kind="ok">上次使用 UDP · QUIC</Tag> : null}
          {transport === 'tcp' ? <Tag kind="info">上次使用 TCP 回退</Tag> : null}
          {!transport ? <Tag>尚无连接记录</Tag> : null}
          {server.disableUdp ? <Tag kind="warn">已禁用 UDP</Tag> : null}
          {server.autoConnect ? <Tag kind="ok">启动时自动连接</Tag> : null}
          <div className="spacer" />
          <span className="small faint">如需登录账号或查看连接状态，请打开左侧「同步服务」页面。</span>
        </div>
      </div>
    </div>
  )
}








