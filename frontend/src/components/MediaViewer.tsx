import { useEffect, useState } from 'react'
import { api } from '../lib/api'
import { Spinner } from './ui'

export interface MediaTarget {
  /** Local absolute path, cloud path or SFTP path depending on source. */
  path: string
  name: string
  kind: 'image' | 'video' | 'audio' | 'text'
  source: 'local' | 'cloud' | 'sftp'
  sessionId?: string
}

function kindOf(name: string): MediaTarget['kind'] | '' {
  const lower = name.toLowerCase()
  if (/\.(png|jpe?g|gif|webp|bmp|svg|ico|avif)$/.test(lower)) return 'image'
  if (/\.(mp4|webm|mkv|mov|m4v|avi|ogv)$/.test(lower)) return 'video'
  if (/\.(mp3|wav|ogg|m4a|flac|aac)$/.test(lower)) return 'audio'
  if (/\.(txt|md|log|json|ya?ml|toml|ini|conf|xml|csv)$/.test(lower)) return 'text'
  return ''
}

export function isPreviewable(name: string): boolean {
  return kindOf(name) !== ''
}

export default function MediaViewer({ target, onClose }: { target: MediaTarget; onClose: () => void }) {
  const [url, setUrl] = useState('')
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError('')
    setUrl('')
    setText('')
    void (async () => {
      try {
        let src = ''
        if (target.source === 'local') {
          src = await api.RegisterMedia(target.path)
        } else if (target.source === 'cloud') {
          src = await api.PreviewCloudMedia(target.path)
        } else if (target.source === 'sftp') {
          if (!target.sessionId) throw new Error('缺少会话')
          src = await api.PreviewSFTPMedia(target.sessionId, target.path)
        }
        if (cancelled) return
        setUrl(src)
        if (target.kind === 'text') {
          const resp = await fetch(src)
          if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
          const body = await resp.text()
          if (!cancelled) setText(body.slice(0, 200_000))
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [target])

  return (
    <div className="media-backdrop" onClick={onClose}>
      <div className="media-panel" onClick={(e) => e.stopPropagation()}>
        <div className="row" style={{ padding: '10px 14px', borderBottom: '1px solid var(--border)' }}>
          <div className="truncate" title={target.path} style={{ flex: 1 }}>
            {target.name}
          </div>
          <span className="small faint" style={{ marginRight: 12 }}>
            {target.kind.toUpperCase()} · {target.source}
          </span>
          <button className="btn btn-sm" onClick={onClose}>
            关闭
          </button>
        </div>
        <div className="media-body">
          {loading ? (
            <Spinner label="加载中" />
          ) : error ? (
            <div className="empty">
              <div className="empty-title">无法预览</div>
              <div className="empty-hint">{error}</div>
            </div>
          ) : target.kind === 'image' ? (
            <img src={url} alt={target.name} className="media-image" />
          ) : target.kind === 'video' ? (
            <video src={url} controls autoPlay className="media-video" />
          ) : target.kind === 'audio' ? (
            <audio src={url} controls autoPlay style={{ width: '100%', marginTop: 40 }} />
          ) : (
            <pre className="media-text">{text}</pre>
          )}
        </div>
      </div>
    </div>
  )
}

export { kindOf }
