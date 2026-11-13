import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, type ProxyInfo, type ProxyRule, type Settings, type TestResult } from '../lib/api'
import { Field, Switch, Tag, useToast } from '../components/ui'
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
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  useEffect(() => {
    void load()
  }, [load])

  const update = (scope: string, patch: Partial<ScopeState>) => {
    setState((prev) => ({ ...prev, [scope]: { ...prev[scope], ...patch } }))
  }

  const updateRule = (scope: string, patch: Partial<ProxyRule>) => {
    setState((prev) => {
      const cur = prev[scope] ?? { rule: normalizeRule(undefined), override: true }
      return { ...prev, [scope]: { ...cur, rule: { ...cur.rule, ...patch }, override: true } }
    })
  }

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
                {st.override ? <Tag kind="info">已覆盖</Tag> : <Tag>跟随默认</Tag>}
                <Switch
                  checked={enabled}
                  title={enabled ? '走代理' : '不走代理'}
                  onChange={(v) =>
                    updateRule(scope, {
                      mode: v ? (st.rule.mode === 'off' || !st.rule.mode ? 'http' : st.rule.mode) : 'off',
                    })
                  }
                />
              </div>

              <div className="row" style={{ gap: 8 }}>
                <select
                  className="select"
                  style={{ width: 130 }}
                  value={st.rule.mode === 'off' ? 'off' : st.rule.mode}
                  onChange={(e) => updateRule(scope, { mode: e.target.value })}
                >
                  <option value="off">不走代理</option>
                  <option value="http">HTTP 代理</option>
                  <option value="socks5">SOCKS5</option>
                </select>
                <input
                  className="input mono"
                  style={{ flex: 1 }}
                  placeholder="host:port"
                  disabled={st.rule.mode === 'off'}
                  value={st.rule.url}
                  onChange={(e) => updateRule(scope, { url: e.target.value })}
                />
              </div>

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
                <button className="btn btn-primary btn-sm" disabled={busy[scope]} onClick={() => save(scope)}>
                  保存
                </button>
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
