import { useCallback, useEffect, useState } from 'react'
import { api } from '../lib/api'
import { Spinner, Tag, useToast } from '../components/ui'

/**
 * DNS resolution settings.
 *
 * Node hostnames and every destination that must be dialled without the proxy
 * are resolved here. In DoH mode there is deliberately NO fallback to the local
 * resolver — a poisoned local answer would silently break the proxy.
 */
export default function DNSCard() {
  const toast = useToast()
  const [mode, setMode] = useState<'system' | 'doh'>('system')
  const [servers, setServers] = useState<Awaited<ReturnType<typeof api.DNSStatus>>['servers']>([])
  const [input, setInput] = useState('')
  const [importOpen, setImportOpen] = useState(false)
  const [bulk, setBulk] = useState('')
  const [busy, setBusy] = useState(false)
  const [probe, setProbe] = useState('')
  const [result, setResult] = useState<{ host: string; mode: string; ips?: string[]; ms?: number; error?: string } | null>(null)

  const load = useCallback(async () => {
    try {
      const st = await api.DNSStatus()
      setMode(st.mode === 'doh' ? 'doh' : 'system')
      setServers(st.servers ?? [])
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  useEffect(() => {
    void load()
  }, [load])

  const apply = async (fn: () => Promise<{ mode: string; servers: any[] }>) => {
    setBusy(true)
    try {
      const st = await fn()
      setMode(st.mode === 'doh' ? 'doh' : 'system')
      setServers((st.servers ?? []) as typeof servers)
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  const add = async (raw: string) => {
    const value = raw.trim()
    if (!value) return
    setBusy(true)
    try {
      const tested = await api.DNSTest(value)
      await apply(() => api.DNSAddServer(value))
      toast.push(`已添加 ${tested.host}（${tested.ms ?? 0}ms）`)
      setInput('')
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card">
      <h3 className="card-title">DNS 解析</h3>
      <p className="card-desc">
        节点的域名解析、以及不走代理的直连解析都在这里决定。<b>自定义 DoH 模式不会回退到本地 DNS</b>：本地解析被污染时也能工作。
      </p>

      <div className="row row-wrap" style={{ gap: 8 }}>
        <select
          className="select"
          style={{ width: 220 }}
          value={mode}
          disabled={busy}
          onChange={(e) => void apply(() => api.DNSSetMode(e.target.value))}
        >
          <option value="system">系统 DNS（本地解析）</option>
          <option value="doh">自定义 DoH（sdns）</option>
        </select>
        {mode === 'system' ? <Tag>使用操作系统解析器</Tag> : <Tag kind="ok">仅走 DoH，不回落本地</Tag>}
        <div className="spacer" />
        {busy ? <Spinner label="" /> : null}
        <button className="btn btn-sm btn-ghost" onClick={() => void apply(() => api.DNSClearCache())} disabled={busy}>
          清空缓存
        </button>
      </div>

      <div className="row row-wrap" style={{ gap: 8, marginTop: 10 }}>
        <input
          className="input mono"
          style={{ flex: 1, minWidth: 320 }}
          placeholder="粘贴 sdns://AgcAAAAAAAAA… 或 https://dns.alidns.com/dns-query"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void add(input)
          }}
        />
        <button className="btn btn-sm btn-primary" disabled={busy || !input.trim()} onClick={() => void add(input)}>
          添加
        </button>
        <button className="btn btn-sm" onClick={() => setImportOpen((v) => !v)}>
          导入
        </button>
      </div>

      {importOpen ? (
        <div className="col" style={{ gap: 6, marginTop: 8 }}>
          <textarea
            className="input mono"
            style={{ minHeight: 96, resize: 'vertical' }}
            placeholder="每行一个 sdns:// 或 https:// 地址，可一次导入多个"
            value={bulk}
            onChange={(e) => setBulk(e.target.value)}
          />
          <div className="row" style={{ gap: 8 }}>
            <button
              className="btn btn-sm"
              disabled={busy || !bulk.trim()}
              onClick={async () => {
                const lines = bulk
                  .split(/\r?\n/)
                  .map((s) => s.trim())
                  .filter(Boolean)
                for (const line of lines) {
                  try {
                    await api.DNSAddServer(line)
                  } catch (err) {
                    toast.error(err)
                  }
                }
                await load()
                setBulk('')
                setImportOpen(false)
                if (lines.length) toast.push(`已导入 ${lines.length} 条`)
              }}
            >
              导入全部
            </button>
            <button className="btn btn-sm btn-ghost" onClick={() => setImportOpen(false)}>
              取消
            </button>
          </div>
        </div>
      ) : null}

      {servers.length > 0 ? (
        <div className="list" style={{ marginTop: 10 }}>
          {servers.map((s) => (
            <div key={s.raw} className="file-row">
              <span className="file-icon">🛰</span>
              <span className="file-name truncate" title={s.raw}>
                {s.host || s.raw}
                {s.error ? <Tag kind="bad">无效</Tag> : null}
              </span>
              <span className="small faint mono">{s.endpoint}</span>
              {s.addr ? <Tag kind="ok">固定 IP {s.addr}</Tag> : null}
              <button
                className="btn btn-ghost btn-sm"
                title="删除"
                disabled={busy}
                onClick={() => void apply(() => api.DNSRemoveServer(s.raw))}
              >
                ✕
              </button>
            </div>
          ))}
        </div>
      ) : (
        <div className="small faint" style={{ marginTop: 8 }}>
          {mode === 'doh' ? '还没有 DoH 服务器，添加一个 sdns:// 才能解析。' : ''}
        </div>
      )}

      <div className="row row-wrap" style={{ gap: 8, marginTop: 12 }}>
        <input
          className="input mono"
          style={{ flex: 1, minWidth: 240 }}
          placeholder="用当前设置解析一个域名，例如 cf.yfjc.sbs"
          value={probe}
          onChange={(e) => setProbe(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== 'Enter') return
            void api.DNSLookup(probe).then(setResult).catch((err) => toast.error(err))
          }}
        />
        <button
          className="btn btn-sm"
          disabled={!probe.trim()}
          onClick={() => void api.DNSLookup(probe).then(setResult).catch((err) => toast.error(err))}
        >
          解析测试
        </button>
      </div>
      {result ? (
        <div className="small" style={{ marginTop: 6 }}>
          <Tag kind={result.error ? 'bad' : 'ok'}>{result.mode === 'doh' ? 'DoH' : '系统'}</Tag>{' '}
          <span className="mono">{result.host}</span>{' '}
          {result.error ? (
            <span style={{ color: 'var(--danger)' }}>{result.error}</span>
          ) : (
            <span className="faint mono">
              → {(result.ips ?? []).join(', ')} {result.ms ? `· ${result.ms}ms` : ''}
            </span>
          )}
        </div>
      ) : null}
    </div>
  )
}