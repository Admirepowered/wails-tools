import { useCallback, useEffect, useState } from 'react'
import { api, type SNIProxySettings, type SNIProxyStatus } from '../lib/api'
import { Field, Spinner, Switch, Tag, useToast } from '../components/ui'

const DEFAULT_SNI_ADDR = '127.0.0.1:17891'

/**
 * Local SNI proxy: allowlisted domains are re-originated to a pinned/resolved
 * IP (optionally with a MITM certificate signed by the local root CA); all
 * other traffic keeps using the app's existing proxy.
 */
export default function SNICard() {
  const toast = useToast()
  const [cfg, setCfg] = useState<SNIProxySettings | null>(null)
  const [status, setStatus] = useState<SNIProxyStatus | null>(null)
  const [rules, setRules] = useState('')
  const [addr, setAddr] = useState(DEFAULT_SNI_ADDR)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const saved = await api.GetSNIProxySettings()
      setCfg(saved)
      setRules(saved.rules)
      setAddr(saved.addr || DEFAULT_SNI_ADDR)
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
    const payload: SNIProxySettings = { ...cfg, ...next, addr, rules }
    setBusy(true)
    try {
      setStatus(await api.SaveSNIProxySettings(payload))
      setCfg(payload)
      toast.push(payload.enabled ? 'SNI 代理已更新' : 'SNI 代理已关闭')
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
            SNI 代理
            {status?.running ? <Tag kind="ok">运行中</Tag> : <Tag>已关闭</Tag>}
            {stats && stats.mitm > 0 ? <Tag kind="info">MITM {stats.mitm}</Tag> : null}
          </h3>
          <p className="card-desc" style={{ margin: 0 }}>
            规则内的域名重新握手到固定/解析出的 IP（保留真实 SNI 与 Host），其余流量继续走上面的代理。
            开启 MITM 需要导入本机自动生成的根证书；不导入也能用，但只会做原始 TLS 转发（不解密）。
          </p>
        </div>
        <Switch checked={!!cfg?.enabled} onChange={(v) => void save({ enabled: v })} />
      </div>

      <div className="grid-2" style={{ marginTop: 12 }}>
        <Field label="监听地址" hint="给浏览器 / 系统代理填写这个地址">
          <div className="row" style={{ gap: 8 }}>
            <input className="input mono" value={addr} onChange={(e) => setAddr(e.target.value)} placeholder={DEFAULT_SNI_ADDR} />
            <button className="btn btn-sm" disabled={busy} onClick={() => void save({ addr })}>
              应用
            </button>
          </div>
        </Field>
        <Field label="出站方式" hint="关闭后直连固定 IP，打开后复用当前代理规则">
          <div className="row" style={{ gap: 10 }}>
            <Switch checked={!!cfg?.useExistingProxy} onChange={(v) => void save({ useExistingProxy: v })} />
            <span className="small faint">{cfg?.useExistingProxy ? '走已有代理' : '直接连接'}</span>
          </div>
        </Field>
      </div>

      <div className="row row-wrap" style={{ gap: 14, marginTop: 6 }}>
        <label className="row" style={{ gap: 8 }}>
          <Switch checked={!!cfg?.mitm} onChange={(v) => void save({ mitm: v })} />
          <span className="small">允许 MITM（需要根证书）</span>
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
        {status?.caPath ? <span className="small faint mono truncate" style={{ maxWidth: 360 }}>{status.caPath}</span> : null}
      </div>

      <div className="divider" />

      <Field
        label="域名规则"
        hint="格式：domain / domain=IP[,IP] / .sub.domain / alias=[alias] / !domain（不做 MITM）。不带 IP 的用 DoH 解析并缓存 3 天。"
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
            连接 {stats.total} · MITM {stats.mitm} · 透传 {stats.passthrough} · 上游 {stats.upstream} · 失败 {stats.failed}
          </span>
        ) : null}
      </div>
      {status?.cachedDomains ? <div className="small faint" style={{ marginTop: 6 }}>DNS 缓存 {status.cachedDomains} 个域名</div> : null}
      {stats?.lastError ? (
        <div className="small" style={{ color: 'var(--danger)', marginTop: 6 }}>
          最近错误：{stats.lastError}
        </div>
      ) : null}
      {status?.running && status.listen ? (
        <div className="small faint" style={{ marginTop: 6 }}>
          实际监听：<span className="mono">{status.listen}</span>（HTTP CONNECT / SOCKS5 均可）
        </div>
      ) : null}
    </div>
  )
}