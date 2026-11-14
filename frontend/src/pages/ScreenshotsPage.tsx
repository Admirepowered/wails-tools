import { useCallback, useEffect, useState } from 'react'
import { api, onEvent, type ScreenshotItem, type ScreenshotSettings } from '../lib/api'
import { Empty, Field, Spinner, Switch, Tag, useToast } from '../components/ui'
import { formatBytes, formatTime } from '../lib/format'
import type { PageProps } from '../App'

export default function ScreenshotsPage(_props: PageProps) {
  const toast = useToast()
  const [items, setItems] = useState<ScreenshotItem[]>([])
  const [thumbs, setThumbs] = useState<Record<string, string>>({})
  const [settings, setSettings] = useState<ScreenshotSettings | null>(null)
  const [displays, setDisplays] = useState(1)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [watching, setWatching] = useState(false)
  const [hotkey, setHotkey] = useState('')
  const [autoUpload, setAutoUpload] = useState(false)

  const load = useCallback(async () => {
    try {
      const [list, cfg, disp, watch] = await Promise.all([
        api.ListScreenshots(60),
        api.GetScreenshotSettings(),
        api.ScreenshotDisplays(),
        api.ClipboardWatching(),
      ])
      setItems(list)
      setSettings(cfg)
      setHotkey(cfg.hotkey ?? '')
      setAutoUpload(!!cfg.autoUpload)
      setDisplays(disp)
      setWatching(watch)
      // Thumbnails are loaded lazily but eagerly enough to feel instant.
      for (const item of list.slice(0, 24)) {
        if (thumbs[item.path]) continue
        try {
          const data = await api.ScreenshotThumbnail(item.path, 360)
          setThumbs((prev) => ({ ...prev, [item.path]: data }))
        } catch {
          /* a corrupt file should not break the gallery */
        }
      }
    } catch (err) {
      toast.error(err)
    } finally {
      setLoading(false)
    }
  }, [toast, thumbs])

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const off = onEvent<ScreenshotItem>('screenshot:saved', (item) => {
      toast.push(`已自动保存：${item.name}`)
      void load()
    })
    const offWatch = onEvent<{ watching: boolean; error?: string }>('screenshot:watch', (ev) => {
      setWatching(!!ev?.watching)
      if (ev?.error) toast.push(ev.error, 'warn')
    })
    return () => {
      off()
      offWatch()
    }
  }, [load, toast])

  const capture = async (display: number) => {
    setBusy('capture')
    try {
      const item = await api.CaptureScreenshot(display)
      setThumbs((prev) => {
        const next = { ...prev }
        delete next[item.path]
        return next
      })
      await load()
      toast.push(`截图已保存：${item.name}`)
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy('')
    }
  }

  const patch = (next: Partial<ScreenshotSettings>) => setSettings((cur) => (cur ? { ...cur, ...next } : cur))

  const saveAutoUpload = async (v: boolean) => {
    if (!settings) return
    const next = { ...settings, autoUpload: v }
    setAutoUpload(v)
    setSettings(next)
    try {
      const saved = await api.SaveScreenshotSettings(next)
      setSettings(saved)
      toast.push(v ? '新截图会自动同步到云端' : '已关闭自动同步')
    } catch (err) {
      toast.error(err)
    }
  }

  const saveSettings = async () => {
    if (!settings) return
    setBusy('save')
    try {
      const saved = await api.SaveScreenshotSettings(settings)
      setSettings(saved)
      setWatching(await api.ClipboardWatching())
      toast.push('截图设置已保存')
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy('')
    }
  }

  return (
    <div>
      <div className="card">
        <div className="row">
          <div>
            <h3 className="card-title">截图自动保存</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              一键截屏并自动保存到指定目录；开启剪贴板监听后，任何复制到剪贴板的图片也会自动落盘。
            </p>
          </div>
          <div className="spacer" />
          {watching ? <Tag kind="ok">剪贴板监听中</Tag> : <Tag>剪贴板监听关闭</Tag>}
          <Tag kind="info">{displays} 台显示器</Tag>
        </div>

        <div className="row" style={{ marginTop: 14, gap: 8 }}>
          <button className="btn btn-primary" disabled={busy !== ''} onClick={() => capture(0)}>
            截取主屏
          </button>
          <button className="btn" disabled={busy !== ''} onClick={() => capture(-1)}>
            截取全部屏幕
          </button>
          <button className="btn" onClick={() => load()}>
            刷新列表
          </button>
          <div className="spacer" />
          <button
            className="btn"
            onClick={async () => {
              try {
                await api.OpenScreenshotDirectory()
              } catch (err) {
                toast.error(err)
              }
            }}
          >
            打开目录
          </button>
        </div>

        {settings && (
          <>
            <div className="divider" />
            <div className="grid-2">
              <Field label="保存目录">
                <div className="row" style={{ gap: 6 }}>
                  <input className="input mono truncate" value={settings.directory} readOnly />
                  <button
                    className="btn btn-sm"
                    onClick={async () => {
                      try {
                        const dir = await api.PickScreenshotDirectory()
                        if (dir) patch({ directory: dir })
                      } catch (err) {
                        toast.error(err)
                      }
                    }}
                  >
                    …
                  </button>
                </div>
              </Field>
              <Field label="命名模板" hint="可用占位符：{date} {time} {datetime} {unix} {source}">
                <input
                  className="input mono"
                  value={settings.pattern}
                  onChange={(e) => patch({ pattern: e.target.value })}
                />
              </Field>
            </div>
            <div className="row row-wrap" style={{ marginTop: 10, gap: 18 }}>
              <label className="row" style={{ gap: 8 }}>
                <Switch checked={settings.autoSave} onChange={(v) => patch({ autoSave: v })} />
                <span className="small">截图后自动保存</span>
              </label>
              <label className="row" style={{ gap: 8 }}>
                <Switch checked={autoUpload} onChange={(v) => void saveAutoUpload(v)} />
                <span className="small">截图自动同步到服务器</span>
              </label>
              <div className="row" style={{ gap: 8 }}>
                <span className="small">全局快捷键</span>
                <input
                  className="input mono"
                  style={{ width: 140 }}
                  value={hotkey}
                  onChange={(e) => setHotkey(e.target.value)}
                  placeholder="Ctrl+Alt+S"
                />
                <button
                  className="btn btn-sm"
                  onClick={async () => {
                    try {
                      const saved = await api.SaveScreenshotHotkey(hotkey)
                      setHotkey(saved)
                      toast.push(saved ? `快捷键已设为 ${saved}` : '已关闭快捷键')
                    } catch (err) {
                      toast.error(err)
                    }
                  }}
                >
                  应用
                </button>
              </div>
              <label className="row" style={{ gap: 8 }}>
                <Switch
                  checked={settings.watchClipboard}
                  onChange={async (v) => {
                    patch({ watchClipboard: v })
                    try {
                      const on = await api.SetClipboardWatch(v)
                      setWatching(on)
                      toast.push(v ? (on ? '已开启剪贴板监听' : '剪贴板监听不可用') : '已关闭剪贴板监听', v && !on ? 'warn' : 'info')
                    } catch (err) {
                      toast.error(err)
                    }
                  }}
                />
                <span className="small">剪贴板图片自动保存</span>
              </label>
              <label className="row" style={{ gap: 8 }}>
                <span className="small faint">格式</span>
                <select
                  className="select"
                  style={{ width: 110 }}
                  value={settings.format}
                  onChange={(e) => patch({ format: e.target.value })}
                >
                  <option value="png">PNG</option>
                  <option value="jpg">JPEG</option>
                </select>
              </label>
              <div className="spacer" />
              <button className="btn btn-primary btn-sm" disabled={busy !== ''} onClick={saveSettings}>
                保存设置
              </button>
            </div>
          </>
        )}
      </div>

      {loading ? (
        <div style={{ padding: 24 }}>
          <Spinner label="加载截图" />
        </div>
      ) : items.length === 0 ? (
        <Empty icon="▣" title="还没有截图" hint="点击「截取主屏」开始，或开启剪贴板监听自动收集" />
      ) : (
        <div className="grid-3" style={{ marginTop: 14 }}>
          {items.map((item) => (
            <div className="card" key={item.path} style={{ padding: 10 }}>
              <div
                style={{
                  height: 140,
                  borderRadius: 8,
                  overflow: 'hidden',
                  background: 'var(--bg-sunken)',
                  display: 'grid',
                  placeItems: 'center',
                  cursor: 'pointer',
                }}
                onClick={async () => {
                  try {
                    await api.OpenPath(item.path)
                  } catch (err) {
                    toast.error(err)
                  }
                }}
              >
                {thumbs[item.path] ? (
                  <img src={thumbs[item.path]} alt={item.name} style={{ maxWidth: '100%', maxHeight: '100%' }} />
                ) : (
                  <span className="faint small">预览加载中…</span>
                )}
              </div>
              <div className="truncate" style={{ marginTop: 8, fontWeight: 540 }} title={item.name}>
                {item.name}
              </div>
              <div className="small faint">
                {formatBytes(item.size)} · {formatTime(item.createdAt)}
                {item.width ? ` · ${item.width}×${item.height}` : ''}
              </div>
              <div className="row" style={{ marginTop: 6, gap: 6 }}>
                <button
                  className="btn btn-sm"
                  onClick={async () => {
                    try {
                      await api.RevealInExplorer(item.path)
                    } catch (err) {
                      toast.error(err)
                    }
                  }}
                >
                  定位
                </button>
                <button
                  className="btn btn-danger btn-sm"
                  onClick={async () => {
                    try {
                      await api.DeleteScreenshot(item.path)
                      await load()
                      toast.push('已删除')
                    } catch (err) {
                      toast.error(err)
                    }
                  }}
                >
                  删除
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
