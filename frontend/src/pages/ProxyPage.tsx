import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  api,
  onEvent,
  type ActiveProxyInfo,
  type DirectRuleSettings,
  type GeoIPStatus,
  type MiproxyNode,
  type ProxyChainSettings,
  type ProxyInfo,
  type ProxyRule,
  type ProxyRuntimeSettings,
  type Settings,
  type TestResult,
} from '../lib/api'
import { Field, Spinner, Switch, Tag, useToast } from '../components/ui'
import { useAutoSave } from '../lib/autosave'
import type { PageProps } from '../App'

const SCOPE_META: Record<string, { title: string; desc: string }> = {
  http: { title: 'HTTP 下载 / 下载工具', desc: '多线程下载、链接解析与内部预览请求' },
  netdisk: { title: '网盘接口', desc: '夸克 / UC / 百度网盘的 API 与直链请求' },
  ssh: { title: 'SSH / SFTP', desc: 'SSH 连接、SFTP 传输（中转隧道不受影响）' },
  ftp: { title: 'FTP / FTPS', desc: 'FTP 控制连接与数据连接' },
  sync: { title: '同步服务连接', desc: '与服务端的同步链路（UDP 优先）' },
}

interface ScopeState {
  rule: ProxyRule
  override: boolean
}

export default function ProxyPage({ settings, reloadSettings }: PageProps) {
  const toast = useToast()
  const [scopes, setScopes] = useState<string[]>([])
  const [state, setState] = useState<Record<string, ScopeState>>({})
  const [tests, setTests] = useState<Record<string, TestResult | null>>({})
  const [busy, setBusy] = useState<Record<string, boolean>>({})
  const [probeUrl, setProbeUrl] = useState('http://www.gstatic.com/generate_204')
  const [dirty, setDirty] = useState<Record<string, boolean>>({})
  const [internal, setInternal] = useState<Record<string, boolean>>({})
  const [mode, setMode] = useState<'off' | 'internal'>('off')
  const [active, setActive] = useState<ActiveProxyInfo | null>(null)
  const [runtimeCfg, setRuntimeCfg] = useState<ProxyRuntimeSettings>({ external: false, port: 7890 })
  const [sysState, setSysState] = useState<{ enabled: boolean; server: string; supported: boolean; setting: boolean } | null>(null)

  const loadRuntime = useCallback(async () => {
    try {
      const [info, cfg, sys] = await Promise.all([
        api.ActiveProxyNode(),
        api.GetProxyRuntimeSettings(),
        api.SystemProxyState(),
      ])
      setActive(info)
      setRuntimeCfg(cfg)
      setSysState(sys)
      setMode(info.mode === 'internal' ? 'internal' : 'off')
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const load = useCallback(async () => {
    try {
      const list = await api.ProxyScopes()
      setScopes(list)
      const entries = await Promise.all(
        list.map(async (scope) => {
          const info: ProxyInfo = await api.GetProxy(scope)
          return [scope, { rule: normalizeRule(info.rule), override: info.override }] as const
        }),
      )
      setState(Object.fromEntries(entries))
      const internalEntries = await Promise.all(
        list.map(async (scope) => [scope, await api.ScopeInternalProxy(scope)] as const),
      )
      setInternal(Object.fromEntries(internalEntries))
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  useEffect(() => {
    void load()
    void loadRuntime()
  }, [load, loadRuntime])

  useEffect(() => {
    const off = onEvent('proxy:status', () => void loadRuntime())
    return () => off()
  }, [loadRuntime])

  const switchMode = async (next: 'off' | 'internal') => {
    try {
      await api.SetProxyMode(next)
      setMode(next)
      await reloadSettings()
      await loadRuntime()
      await load()
      toast.push(next === 'internal' ? '已切换到内部代理' : '已关闭代理，恢复直连')
    } catch (err) {
      toast.error(err)
      await loadRuntime()
    }
  }

  const saveRuntimeCfg = async () => {
    try {
      const saved = await api.SaveProxyRuntimeSettings(runtimeCfg)
      setRuntimeCfg(saved)
      await loadRuntime()
      toast.push('本地代理设置已保存')
    } catch (err) {
      toast.error(err)
    }
  }

  const toggleSystemProxy = async (enabled: boolean) => {
    try {
      await api.SetSystemProxy(enabled)
      await loadRuntime()
      toast.push(enabled ? '已开启系统代理' : '已关闭系统代理')
    } catch (err) {
      toast.error(err)
      await loadRuntime()
    }
  }

  const update = (scope: string, patch: Partial<ScopeState>) => {
    setState((prev) => ({ ...prev, [scope]: { ...prev[scope], ...patch } }))
    setDirty((prev) => ({ ...prev, [scope]: true }))
  }

  const updateRule = (scope: string, patch: Partial<ProxyRule>) => {
    setState((prev) => {
      const cur = prev[scope] ?? { rule: normalizeRule(undefined), override: true }
      return { ...prev, [scope]: { ...cur, rule: { ...cur.rule, ...patch }, override: true } }
    })
    setDirty((prev) => ({ ...prev, [scope]: true }))
  }

  // Seamless save: dirty scopes are persisted shortly after editing stops.
  useEffect(() => {
    const scopes = Object.keys(dirty).filter((k) => dirty[k])
    if (scopes.length === 0) return
    const timer = window.setTimeout(() => {
      setDirty({})
      for (const scope of scopes) {
        const st = state[scope]
        if (!st) continue
        void api
          .SetProxy(scope, { ...st.rule, mode: st.rule.mode === 'inherit' ? 'off' : st.rule.mode })
          .then(() => reloadSettings())
          .catch((err) => toast.error(err))
      }
    }, 700)
    return () => window.clearTimeout(timer)
  }, [dirty, state, reloadSettings, toast])

  // The engine settings save themselves too.
  useAutoSave(runtimeCfg, async (next) => {
    const saved = await api.SaveProxyRuntimeSettings(next)
    setRuntimeCfg(saved)
  }, { delay: 700, onError: (err) => toast.error(err) })

  const save = async (scope: string) => {
    const st = state[scope]
    if (!st) return
    setBusy((b) => ({ ...b, [scope]: true }))
    try {
      await api.SetProxy(scope, { ...st.rule, mode: st.rule.mode === 'inherit' ? 'off' : st.rule.mode })
      await reloadSettings()
      await load()
      toast.push('代理设置已保存')
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy((b) => ({ ...b, [scope]: false }))
    }
  }

  const followDefault = async (scope: string) => {
    setBusy((b) => ({ ...b, [scope]: true }))
    try {
      await api.SetProxy(scope, { mode: 'inherit', url: '' })
      await reloadSettings()
      await load()
      toast.push('已恢复跟随全局默认')
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy((b) => ({ ...b, [scope]: false }))
    }
  }

  const test = async (scope: string) => {
    setBusy((b) => ({ ...b, [scope]: true }))
    setTests((t) => ({ ...t, [scope]: null }))
    try {
      const st = state[scope]
      const rule = st?.rule
      const res = rule && rule.mode !== 'off' ? await api.TestProxyRule(rule, probeUrl) : await api.TestProxy(scope, probeUrl)
      setTests((t) => ({ ...t, [scope]: res }))
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy((b) => ({ ...b, [scope]: false }))
    }
  }

  const applyAll = async (viaProxy: boolean) => {
    try {
      if (!viaProxy) {
        await api.DisableAllProxies()
        toast.push('已全部切换为直连')
      } else {
        const first = Object.values(state).find((s) => s.rule.mode !== 'off' && s.rule.url)
        if (!first) {
          toast.push('请先为任一类型填写代理地址', 'warn')
          return
        }
        await api.SetAllProxies(first.rule)
        toast.push('已把该代理应用到全部类型')
      }
      await reloadSettings()
      await load()
    } catch (err) {
      toast.error(err)
    }
  }

  const summary = useMemo(() => {
    const total = Object.keys(state).length
    const proxied = Object.values(state).filter((s) => s.rule.mode !== 'off' && s.rule.url).length
    return { total, proxied }
  }, [state])

  return (
    <div className="page-narrow">
      <div className="card">
        <div className="row">
          <div>
            <h3 className="card-title">代理总览</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              {summary.proxied === 0
                ? '当前全部连接直连（不走代理）'
                : `${summary.proxied} / ${summary.total} 类连接通过代理`}
              {settings?.proxy?.default?.mode && settings.proxy.default.mode !== 'off'
                ? ` · 全局默认：${settings.proxy.default.mode} ${settings.proxy.default.url}`
                : ''}
            </p>
          </div>
          <div className="spacer" />
          <button className="btn" onClick={() => applyAll(false)}>
            全部直连
          </button>
          <button className="btn" onClick={() => applyAll(true)}>
            应用到全部
          </button>
        </div>
        <div className="divider" />
        <div className="row row-wrap">
          <div style={{ minWidth: 320, flex: 1 }}>
            <Field label="测试目标地址" hint="用于验证代理是否可用，默认探测 Google 的 204 接口">
              <input className="input mono" value={probeUrl} onChange={(e) => setProbeUrl(e.target.value)} />
            </Field>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="row">
          <div>
            <h3 className="card-title">本地代理</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              这里只控制本地监听与系统代理；每个工具是否走内部代理，请在各工具卡片里单独开启。
            </p>
          </div>
          <div className="spacer" />
        </div>

        <div className="row row-wrap" style={{ marginTop: 10, gap: 10 }}>
          {active?.running && active.node ? (
            <>
              <Tag kind="ok">当前节点：{active.node.name}</Tag>
              <span className="small faint mono">{active.addr}</span>
              {active.external ? <Tag kind="warn">外部可用</Tag> : <Tag>仅本机</Tag>}
            </>
          ) : (
            <Tag>未启用节点（在「订阅」页点节点上的「启用」）</Tag>
          )}
        </div>

        <div className="divider" />

        <div className="row row-wrap" style={{ gap: 16 }}>
          <label className="row" style={{ gap: 8 }}>
            <Switch checked={runtimeCfg.external} onChange={(v) => setRuntimeCfg({ ...runtimeCfg, external: v })} />
            <span className="small">外部可用（0.0.0.0，供其它设备使用）</span>
          </label>
          <div className="field" style={{ width: 120 }}>
            <input
              className="input"
              type="number"
              min={1024}
              max={65535}
              value={runtimeCfg.port}
              onChange={(e) => setRuntimeCfg({ ...runtimeCfg, port: Number(e.target.value) || 7890 })}
            />
          </div>
          <div className="field" style={{ width: 150 }}>
            <input
              className="input"
              placeholder="用户名（可选）"
              value={runtimeCfg.username ?? ''}
              onChange={(e) => setRuntimeCfg({ ...runtimeCfg, username: e.target.value })}
            />
          </div>
          <div className="field" style={{ width: 150 }}>
            <input
              className="input"
              type="password"
              placeholder="密码（可选）"
              value={runtimeCfg.password ?? ''}
              onChange={(e) => setRuntimeCfg({ ...runtimeCfg, password: e.target.value })}
            />
          </div>

          <div className="spacer" />
          <label className="row" style={{ gap: 8 }} title={runtimeCfg.external ? '把系统代理指向本机代理' : '需先打开「外部可用」'}>
            <Switch
              checked={sysState?.setting ?? false}
              onChange={toggleSystemProxy}
              title={runtimeCfg.external ? '' : '需先打开外部可用'}
            />
            <span className="small">系统代理</span>
          </label>
          {sysState ? (
            sysState.supported ? (
              <Tag kind={sysState.enabled ? 'ok' : 'default'}>
                {sysState.enabled ? `已开启 ${sysState.server}` : '未开启'}
              </Tag>
            ) : (
              <Tag kind="warn">当前平台不支持</Tag>
            )
          ) : null}
        </div>
        {!runtimeCfg.external ? (
          <div className="small faint" style={{ marginTop: 6 }}>开启「外部可用」后才能打开系统代理。</div>
        ) : null}
      </div>

      <div className="grid-2" style={{ marginTop: 14 }}>
        <ChainCard onChanged={() => void loadRuntime()} />
        <DirectRulesCard onChanged={() => void loadRuntime()} />
      </div>

      <div className="grid-2" style={{ marginTop: 14 }}>
        {scopes.map((scope) => {
          const st = state[scope] ?? { rule: normalizeRule(undefined), override: false }
          const enabled = st.rule.mode !== 'off' && !!st.rule.url
          const meta = SCOPE_META[scope] ?? { title: scope, desc: '' }
          const t = tests[scope]
          return (
            <div className="scope-card" key={scope}>
              <div className="scope-head">
                <div style={{ flex: 1 }}>
                  <div className="scope-title">{meta.title}</div>
                  <div className="scope-desc">{meta.desc}</div>
                </div>
                <select
                  className="select"
                  style={{ width: 150 }}
                  value={internal[scope] ? 'internal' : st.rule.mode === 'off' || !st.rule.mode ? 'off' : st.rule.mode}
                  onChange={async (e) => {
                    const next = e.target.value
                    if (next === 'internal') {
                      if (!active?.running) {
                        toast.push('请先在订阅页启用一个节点', 'warn')
                        return
                      }
                      try {
                        await api.SetScopeInternalProxy(scope, true)
                        await reloadSettings()
                        await load()
                      } catch (err) {
                        toast.error(err)
                      }
                      return
                    }
                    if (internal[scope]) {
                      try {
                        await api.SetScopeInternalProxy(scope, false)
                      } catch (err) {
                        toast.error(err)
                        return
                      }
                    }
                    updateRule(scope, { mode: next })
                  }}
                >
                  <option value="off">不走代理</option>
                  <option value="http">HTTP 代理</option>
                  <option value="socks5">SOCKS5 代理</option>
                  <option value="internal">内部代理</option>
                </select>
              </div>

              {!internal[scope] && st.rule.mode !== 'off' && (
                <div className="row" style={{ gap: 8 }}>
                  <input
                    className="input mono"
                    style={{ flex: 1 }}
                    placeholder="host:port"
                    value={st.rule.url}
                    onChange={(e) => updateRule(scope, { url: e.target.value })}
                  />
                </div>
              )}

              {internal[scope] && (
                <div className="small faint mono">使用当前节点 {active?.addr ?? ''}</div>
              )}

              {st.rule.mode !== 'off' && (
                <>
                  <div className="row" style={{ gap: 8 }}>
                    <input
                      className="input"
                      placeholder="用户名（可选）"
                      value={st.rule.username ?? ''}
                      onChange={(e) => updateRule(scope, { username: e.target.value })}
                    />
                    <input
                      className="input"
                      type="password"
                      placeholder="密码（可选）"
                      value={st.rule.password ?? ''}
                      onChange={(e) => updateRule(scope, { password: e.target.value })}
                    />
                  </div>
                  <Field label="跳过代理的主机" hint="逗号分隔，支持后缀匹配与 CIDR，例如 192.168.0.0/16, .internal">
                    <input
                      className="input mono"
                      value={(st.rule.noProxy ?? []).join(', ')}
                      onChange={(e) =>
                        updateRule(scope, {
                          noProxy: e.target.value
                            .split(',')
                            .map((s) => s.trim())
                            .filter(Boolean),
                        })
                      }
                    />
                  </Field>
                </>
              )}

              <div className="row">

                <button className="btn btn-sm" disabled={!st.override || busy[scope]} onClick={() => followDefault(scope)}>
                  跟随默认
                </button>
                <button className="btn btn-sm" disabled={busy[scope]} onClick={() => test(scope)}>
                  测试
                </button>
                <div className="spacer" />
                {t ? (
                  <span className={`tag ${t.ok ? 'tag-ok' : 'tag-bad'}`} title={t.detail}>
                    {t.ok ? `可用 ${t.latencyMs}ms` : '不可用'}
                  </span>
                ) : null}
              </div>
              {t && !t.ok ? <div className="small faint mono truncate">{t.detail}</div> : null}
            </div>
          )
        })}
      </div>
    </div>
  )
}

function normalizeRule(rule?: ProxyRule): ProxyRule {
  if (!rule) return { mode: 'off', url: '', username: '', password: '', noProxy: [] }
  return {
    mode: rule.mode || 'off',
    url: rule.url || '',
    username: rule.username || '',
    password: rule.password || '',
    noProxy: rule.noProxy ?? [],
  }
}





/* ---------------- chain ---------------- */

function ChainCard({ onChanged }: { onChanged: () => void }) {
  const toast = useToast()
  const [cfg, setCfg] = useState<ProxyChainSettings>({ mode: 'off', nodeIds: [] })
  const [nodes, setNodes] = useState<MiproxyNode[]>([])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    void (async () => {
      try {
        const [chain, custom] = await Promise.all([api.GetProxyChain(), api.ListCustomNodes()])
        setCfg({ mode: chain.mode || 'off', nodeIds: chain.nodeIds ?? [] })
        setNodes(custom)
      } catch (err) {
        toast.error(err)
      }
    })()
  }, [toast])

  const save = async (next: ProxyChainSettings) => {
    setBusy(true)
    try {
      const saved = await api.SaveProxyChain(next)
      setCfg({ mode: saved.mode, nodeIds: saved.nodeIds ?? [] })
      onChanged()
      toast.push('代理链路已更新')
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  const toggleNode = (id: string) => {
    const cur = cfg.nodeIds ?? []
    let next: string[]
    if (cur.includes(id)) {
      next = cur.filter((x) => x !== id)
    } else {
      if (cur.length >= 2) {
        toast.push('最多选择两个中转节点', 'warn')
        return
      }
      next = [...cur, id]
    }
    void save({ ...cfg, nodeIds: next })
  }

  return (
    <div className="card">
      <h3 className="card-title">代理链路</h3>
      <p className="card-desc">可让出口先经过同步服务器或已有节点中转，最多 2 跳。</p>
      <div className="row" style={{ gap: 8 }}>
        <select
          className="select"
          style={{ width: 200 }}
          value={cfg.mode}
          onChange={(e) => void save({ ...cfg, mode: e.target.value })}
        >
          <option value="off">直连（不中转）</option>
          <option value="server">从服务器跳转</option>
          <option value="nodes">用节点中转</option>
        </select>
        {busy ? <Spinner label="" /> : null}
      </div>
      {cfg.mode === 'nodes' ? (
        <div className="col" style={{ marginTop: 10, gap: 6, maxHeight: 180, overflow: 'auto' }}>
          {nodes.length === 0 ? (
            <div className="small faint">还没有自定义节点，可先在「订阅」页导入。</div>
          ) : (
            nodes.map((n) => {
              const order = (cfg.nodeIds ?? []).indexOf(n.id ?? '')
              return (
                <label key={n.id} className="row" style={{ gap: 8 }}>
                  <input type="checkbox" checked={order >= 0} onChange={() => toggleNode(n.id ?? '')} />
                  <span className="truncate" style={{ flex: 1 }}>
                    {n.name}
                  </span>
                  <span className="small faint mono">
                    {n.server}:{n.port}
                  </span>
                  {order >= 0 ? <Tag kind="info">第 {order + 1} 跳</Tag> : null}
                </label>
              )
            })
          )}
        </div>
      ) : null}
      {cfg.mode === 'server' ? <div className="small faint" style={{ marginTop: 8 }}>需要连接同步服务器；未连接时会自动退回直连。</div> : null}
    </div>
  )
}

/* ---------------- direct rules ---------------- */

function DirectRulesCard({ onChanged }: { onChanged: () => void }) {
  const toast = useToast()
  const [cfg, setCfg] = useState<DirectRuleSettings>({ useGeoIP: false, countries: [], rules: [] })
  const [geo, setGeo] = useState<GeoIPStatus | null>(null)
  const [countries, setCountries] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [ruleText, setRuleText] = useState('')

  useEffect(() => {
    void (async () => {
      try {
        const [rules, status] = await Promise.all([api.GetDirectRules(), api.GeoIPStatus()])
        setCfg({ useGeoIP: rules.useGeoIP, countries: rules.countries ?? [], rules: rules.rules ?? [] })
        setRuleText((rules.rules ?? []).join('\n'))
        setGeo(status)
        if (status.ready) setCountries(await api.GeoIPCountries())
      } catch (err) {
        toast.error(err)
      }
    })()
  }, [toast])

  const save = async (next: DirectRuleSettings) => {
    try {
      await api.SaveDirectRules(next)
      setCfg(next)
      onChanged()
    } catch (err) {
      toast.error(err)
    }
  }

  const download = async () => {
    setBusy(true)
    try {
      const status = await api.DownloadGeoIP()
      setGeo(status)
      setCountries(await api.GeoIPCountries())
      toast.push('geoip 数据库已更新')
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy(false)
    }
  }

  const selected = cfg.countries ?? []
  const pickerList = (() => {
    const common = ['CN', 'HK', 'TW', 'JP', 'US', 'SG', 'KR', 'RU', 'GB', 'DE', 'CA', 'AU']
    const all = [...common, ...countries, ...selected]
    return all.filter((code, i) => all.indexOf(code) === i)
  })()
  return (
    <div className="card">
      <div className="row">
        <h3 className="card-title" style={{ margin: 0 }}>
          直连规则
          {geo?.ready ? <Tag kind="ok">geoip 已就绪</Tag> : <Tag kind="warn">未下载</Tag>}
        </h3>
        <div className="spacer" />
        <button className="btn btn-sm" disabled={busy} onClick={download}>
          {busy ? '下载中…' : geo?.ready ? '更新 geoip' : '下载 geoip'}
        </button>
      </div>
      <p className="card-desc">命中的目标直接连接，不走代理。</p>
      <label className="row" style={{ gap: 8 }}>
        <Switch checked={cfg.useGeoIP} onChange={(v) => void save({ ...cfg, useGeoIP: v })} />
        <span className="small">按国家 IP 直连（geoip.metadb）</span>
      </label>
      {cfg.useGeoIP ? (
        <div className="row row-wrap" style={{ gap: 6, marginTop: 8, maxHeight: 160, overflow: 'auto' }}>
          {pickerList.map((code) => {
            const on = selected.includes(code)
            return (
              <button
                key={code}
                className={`btn btn-sm ${on ? 'btn-primary' : ''}`}
                onClick={() => void save({ ...cfg, countries: on ? selected.filter((c) => c !== code) : [...selected, code] })}
              >
                {code}
              </button>
            )
          })}
        </div>
      ) : null}
      <Field label="自定义直连规则（每行一个域名后缀 / IP / CIDR）" hint="例如 example.com、10.0.0.0/8、192.168.1.1">
        <textarea
          className="textarea"
          style={{ minHeight: 80 }}
          value={ruleText}
          onChange={(e) => setRuleText(e.target.value)}
          onBlur={() => void save({ ...cfg, rules: ruleText.split('\n').map((s) => s.trim()).filter(Boolean) })}
          placeholder="cn.example.com&#10;10.0.0.0/8"
        />
      </Field>
    </div>
  )
}



