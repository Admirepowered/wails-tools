import { useCallback, useEffect, useState } from 'react'
import { api, type SNIProxySettings, type SNIProxyStatus } from '../lib/api'
import { Field, Spinner, Switch, Tag, useToast } from '../components/ui'

/**
 * SNI engine inside the local proxy: rules-covered TLS tunnels are served by the
 * SNI engine (pinned IP, outbound handshake without SNI); everything else keeps
 * following the normal proxy chain. No extra listening port.
 */
export default function SNICard() {
  const toast = useToast()
  const [cfg, setCfg] = useState<SNIProxySettings | null>(null)
  const [status, setStatus] = useState<SNIProxyStatus | null>(null)
  const [rules, setRules] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const saved = await api.GetSNIProxySettings()
      setCfg(saved)
      setRules(saved.rules)
      setStatus(await api.SNIProxyStatus())
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (!status?.running) return
    const timer = setInterval(() => {
      void api
        .SNIProxyStatus()
        .then(setStatus)
        .catch(() => {})
    }, 3000)
    return () => clearInterval(timer)
  }, [status?.running])

  const save = async (next: Partial<SNIProxySettings>) => {
    if (!cfg) return
    const payload: SNIProxySettings = { ...cfg, ...next, rules }
    setBusy(true)
    try {
      setStatus(await api.SaveSNIProxySettings(payload))
      setCfg(payload)
      toast.push(payload.enabled ? 'SNI 分流已更新，随本地代理生效' : '已关闭 SNI 分流，所有流量走代理链路')
    } catch (err) {
      toast.error(err)
      void load()
    } finally {
      setBusy(false)
    }
  }

  const installCA = async () => {
    try {
      const res = await api.InstallSNIProxyCA()
      toast.push(res?.installed ? '根证书已导入（当前用户 · 受信任的根证书颁发机构）' : `请手动导入：${res?.caPath ?? ''}`)
      setStatus(await api.SNIProxyStatus())
    } catch (err) {
      toast.error(err)
    }
  }

  const copyCA = async () => {
    try {
      const path = await api.SNIProxyCAPath()
      await navigator.clipboard.writeText(path)
      toast.push('已复制根证书路径')
    } catch (err) {
      toast.error(err)
    }
  }

  const stats = status?.stats

  return (
    <div className="card">
      <div className="row">
        <div style={{ flex: 1 }}>
          <h3 className="card-title">
            SNI 分流
            {status?.running ? <Tag kind="ok">已启用</Tag> : <Tag>已关闭</Tag>}
            {stats && stats.mitm > 0 ? <Tag kind="info">MITM {stats.mitm}</Tag> : null}
          </h3>
          <p className="card-desc" style={{ margin: 0 }}>
            内置在本地代理里，不额外开端口：命中规则的域名由 SNI 引擎接管（重新握手到固定/解析出的 IP，且出站握手<b>不发送 SNI</b>），
            其余流量照常走上面的代理链路。开启时要解密客户端 TLS，需要先导入根证书。
          </p>
        </div>
        <Switch checked={!!cfg?.enabled} onChange={(v) => void save({ enabled: v })} />
      </div>

      <div className="row row-wrap" style={{ gap: 14, marginTop: 10 }}>
        <label className="row" style={{ gap: 8 }}>
          <Switch checked={!!cfg?.mitm} onChange={(v) => void save({ mitm: v })} />
          <span className="small">允许 MITM（需要根证书）</span>
        </label>
        <label className="row" style={{ gap: 8 }}>
          <Switch checked={!!cfg?.useExistingProxy} onChange={(v) => void save({ useExistingProxy: v })} />
          <span className="small">SNI 出站也走代理链路</span>
        </label>
        <button className="btn btn-sm" onClick={() => void installCA()}>
          导入根证书
        </button>
        <button className="btn btn-ghost btn-sm" onClick={() => void copyCA()}>
          复制证书路径
        </button>
        <button className="btn btn-ghost btn-sm" onClick={() => void api.OpenSNIProxyFolder().catch((err) => toast.error(err))}>
          打开证书目录
        </button>
      </div>
      {status?.caPath ? (
        <div className="small faint mono truncate" style={{ marginTop: 6 }}>
          {status.caPath}
        </div>
      ) : null}
      {status?.proxyAddr ? (
        <div className="small faint" style={{ marginTop: 6 }}>
          与本地代理共用端口：<span className="mono">{status.proxyAddr}</span>
          {status.running ? '' : '（代理未运行，开启代理后自动生效）'}
        </div>
      ) : null}

      <div className="divider" />

      <Field
        label="域名规则"
        hint="格式：domain / domain=IP[,IP] / .sub.domain / alias=[alias] / !domain（原始透传，不 MITM）。不带 IP 的用 DoH 解析并缓存 3 天。只有这里的域名才走 SNI，其余一律走代理链路。"
      >
        <textarea
          className="input mono"
          style={{ minHeight: 180, resize: 'vertical', lineHeight: 1.5 }}
          value={rules}
          onChange={(e) => setRules(e.target.value)}
          spellCheck={false}
        />
      </Field>
      <div className="row" style={{ marginTop: 8, gap: 8 }}>
        <button className="btn btn-sm" disabled={busy} onClick={() => void save({})}>
          {busy ? '保存中…' : '保存规则'}
        </button>
        <button
          className="btn btn-ghost btn-sm"
          onClick={async () => {
            try {
              const reset = await api.ResetSNIProxyRules()
              setRules(reset.rules)
              await load()
              toast.push('已恢复默认规则')
            } catch (err) {
              toast.error(err)
            }
          }}
        >
          恢复默认
        </button>
        {busy ? <Spinner label="" /> : null}
        <div className="spacer" />
        {stats ? (
          <span className="small faint">
            接管 {stats.total} · MITM {stats.mitm} · 透传 {stats.passthrough} · 失败 {stats.failed}
          </span>
        ) : null}
      </div>
      {status?.cachedDomains ? <div className="small faint" style={{ marginTop: 6 }}>DNS 缓存 {status.cachedDomains} 个域名</div> : null}
      {stats?.lastError ? (
        <div className="small" style={{ color: 'var(--danger)', marginTop: 6 }}>
          最近错误：{stats.lastError}
        </div>
      ) : null}
    </div>
  )
}