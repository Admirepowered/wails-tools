import { useCallback, useEffect, useState } from 'react'
import { api, type P2PFilesEntry, type P2PStatus } from '../lib/api'
import { Empty, Spinner, useToast } from '../components/ui'
import { formatBytes } from '../lib/format'

/**
 * Browse / upload / download files on another device over a punched (or
 * relayed) link. Lives under 文件 → 设备文件; both paths share the same Channel.
 */
export default function DeviceFilesCard() {
  const toast = useToast()
  const [p2p, setP2p] = useState<P2PStatus | null>(null)
  const [device, setDevice] = useState('')
  const [path, setPath] = useState('')
  const [root, setRoot] = useState('')
  const [entries, setEntries] = useState<P2PFilesEntry[]>([])
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    api
      .P2PStatus()
      .then(setP2p)
      .catch(() => setP2p(null))
  }, [])

  const load = useCallback(
    async (next: string) => {
      if (!device) return
      setLoading(true)
      try {
        const res = await api.P2PFilesList(device, next)
        setEntries(res.entries ?? [])
        setRoot(res.root ?? '')
        setPath(next)
      } catch (err) {
        toast.error(err)
      } finally {
        setLoading(false)
      }
    },
    [device, toast],
  )

  useEffect(() => {
    if (device) void load('')
  }, [device, load])

  return (
    <div className="card">
      <h3 className="card-title">设备文件（打洞直连）</h3>
      <p className="card-desc" style={{ marginBottom: 8 }}>
        浏览、上传、下载已打洞设备的文件。数据走直连 QUIC（或服务器中转），不经过同步服务器；路径可留空＝对方下载目录，也可直接输入绝对路径（例如 <span className="mono">D:\Media</span>）切换盘符。
      </p>
      <div className="row" style={{ gap: 8, marginBottom: 8 }}>
        <select
          className="select"
          value={device}
          onChange={(e) => {
            setDevice(e.target.value)
            setPath('')
          }}
          style={{ width: 200, flex: '0 0 auto' }}
        >
          <option value="">选择已直连设备</option>
          {(p2p?.connected ?? []).map((d) => (
            <option key={d} value={d}>
              {d.slice(0, 8)}
            </option>
          ))}
        </select>
        <input
          className="input mono"
          style={{ flex: 1, minWidth: 260 }}
          value={path}
          onChange={(e) => setPath(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && void load(path)}
          placeholder="留空＝对方下载目录，也可输入 D:\Media"
        />
        <button className="btn btn-sm" disabled={!device} onClick={() => void load(path)}>
          打开
        </button>
        <button
          className="btn btn-sm btn-ghost"
          disabled={!device}
          onClick={() => {
            const parent = path.replace(/[\\/][^\\/]*$/, '')
            void load(parent === path ? '' : parent)
          }}
        >
          上级
        </button>
        <button className="btn btn-sm btn-ghost" disabled={!device} onClick={() => void load('')}>
          默认目录
        </button>
      </div>
      {root ? (
        <div className="small faint mono truncate" style={{ marginBottom: 6 }}>
          对方当前目录：{path || root}
        </div>
      ) : null}
      {loading ? (
        <Spinner label="读取中…" />
      ) : entries.length === 0 ? (
        <Empty
          title={device ? '目录为空' : '先选择一个已直连设备'}
          hint={device ? '' : '在「同步服务 → 我的设备」里打洞或中转成功后即可浏览'}
        />
      ) : (
        <div className="list">
          {entries.map((e) => (
            <div key={e.path} className="file-row">
              <span className="file-icon">{e.dir ? '📁' : '📄'}</span>
              <span
                className="file-name truncate"
                style={{ cursor: e.dir ? 'pointer' : 'default' }}
                onClick={() => e.dir && void load(e.path)}
              >
                {e.name}
              </span>
              <span className="file-size">{e.dir ? '' : formatBytes(e.size)}</span>
              {!e.dir ? (
                <button
                  className="btn btn-ghost btn-sm"
                  onClick={async () => {
                    try {
                      const dir = await api.PickDirectory('选择保存目录')
                      if (!dir) return
                      const local = `${dir}\\${e.name}`
                      const res = await api.P2PFilesDownload(device, e.path, local)
                      toast.push(`已下载 ${formatBytes(res.bytes)} → ${res.path}`)
                    } catch (err) {
                      toast.error(err)
                    }
                  }}
                >
                  下载
                </button>
              ) : null}
              <button
                className="btn btn-ghost btn-sm"
                title="删除对方文件"
                onClick={async () => {
                  try {
                    await api.P2PFilesRemove(device, e.path)
                    await load(path)
                  } catch (err) {
                    toast.error(err)
                  }
                }}
              >
                ✕
              </button>
            </div>
          ))}
        </div>
      )}
      <div className="row" style={{ gap: 8, marginTop: 10 }}>
        <button
          className="btn btn-sm btn-primary"
          disabled={!device}
          onClick={async () => {
            try {
              const file = await api.PickFile('选择要上传的文件')
              if (!file) return
              const name = file.split(/[\\/]/).pop() ?? 'upload.bin'
              const base = path.replace(/[\\/]+$/, '')
              const target = base ? `${base}/${name}` : name
              const res = await api.P2PFilesUpload(device, file, target)
              toast.push(`已上传 ${formatBytes(res.bytes)} → ${target}`)
              await load(path)
            } catch (err) {
              toast.error(err)
            }
          }}
        >
          上传到对方
        </button>
        <button className="btn btn-sm btn-ghost" disabled={!device} onClick={() => void load(path)}>
          刷新
        </button>
        <div className="spacer" />
        <span className="small faint">
          {(p2p?.connected ?? []).length === 0 ? '当前没有已直连设备' : `已直连 ${(p2p?.connected ?? []).length} 台`}
        </span>
      </div>
    </div>
  )
}
