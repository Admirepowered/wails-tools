import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  api,
  onEvent,
  type MiproxyNode,
  type ProxyRuntimeSettings,
  type ProxyRuntimeStatus,
  type Settings,
  type SubscriptionSource,
  type TestResult,
} from '../lib/api'
import { Confirm, Empty, Field, Modal, Progress, Spinner, Switch, Tag, useToast } from '../components/ui'
import { formatBytes, formatTime, relativeTime } from '../lib/format'
import type { PageProps } from '../App'

const PROTOCOL_LABEL: Record<string, string> = {
  vmess: 'VMess',
  vless: 'VLESS',
  trojan: 'Trojan',
  shadowsocks: 'SS',
  http: 'HTTP',
  socks5: 'SOCKS5',
  direct: '直连',
}

const SCOPE_LABEL: Record<string, string> = {
  http: 'HTTP 下载',
  netdisk: '网盘接口',
  ssh: 'SSH / SFTP',
  ftp: 'FTP',
}

function protocolTag(protocol: string) {
  const label = PROTOCOL_LABEL[protocol] ?? protocol
  const kind = protocol === 'vmess' || protocol === 'vless' || protocol === 'trojan' || protocol === 'shadowsocks' ? 'info' : 'default'
  return <Tag kind={kind}>{label}</Tag>
}

export default function SubscriptionPage({ reloadSettings }: PageProps) {
  const toast = useToast()
  const [list, setList] = useState<SubscriptionSource[]>([])
  const [customNodes, setCustomNodes] = useState<MiproxyNode[]>([])
  const [runtime, setRuntime] = useState<ProxyRuntimeStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [selected, setSelected] = useState('')
  const [editing, setEditing] = useState<SubscriptionSource | null>(null)
  const [deleting, setDeleting] = useState<SubscriptionSource | null>(null)
  const [nodeModal, setNodeModal] = useState<MiproxyNode | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [importText, setImportText] = useState('')

  const [useProxy, setUseProxy] = useState(false)
  const [tests, setTests] = useState<Record<string, { ok: boolean; latencyMs?: number; error?: string } | null>>({})
  useEffect(() => {
    void api
      .GetSettings()
      .then((s) => setUseProxy(!!s.subscriptions?.useProxy))
      .catch(() => {})
  }, [])

  const load = useCallback(async () => {
    try {
      const [items, custom, status] = await Promise.all([
        api.ListSubscriptions(),
        api.ListCustomNodes(),
        api.ProxyRuntimeStatus(),
      ])
      setList(items)
      setCustomNodes(custom)
      setRuntime(status)
      setSelected((prev) => (prev && items.some((i) => i.id === prev) ? prev : items[0]?.id ?? ''))
    } catch (err) {
      toast.error(err)
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    void load()
    void (async () => {
      try {
        const saved = await api.LoadLatencyResults()
        const map: Record<string, { ok: boolean; latencyMs: number; error?: string }> = {}
        for (const [key, entry] of Object.entries(saved ?? {})) {
          map[key] = { ok: entry.ok, latencyMs: entry.latencyMs, error: entry.error }
        }
        setTests((prev) => ({ ...map, ...prev }))
      } catch {
        /* 历史测速结果可选 */
      }
    })()
  }, [load])

  useEffect(() => {
    const off = onEvent<{ source: SubscriptionSource }>('subscription:updated', () => void load())
    const offStatus = onEvent<ProxyRuntimeStatus>('proxy:status', (st) => setRuntime(st))
    return () => {
      off()
      offStatus()
    }
  }, [load])

  const current = list.find((s) => s.id === selected) ?? null
  const nodes = current?.nodes ?? []

  const refreshOne = async (src: SubscriptionSource) => {
    setBusy(src.id)
    try {
      const updated = await api.RefreshSubscription(src.id)
      toast.push(`订阅「${updated.name}」已更新：${updated.nodes.length} 个节点`)
      await load()
    } catch (err) {
      toast.error(err)
      await load()
    } finally {
      setBusy('')
    }
  }

  const refreshAll = async () => {
    setBusy('all')
    try {
      await api.RefreshAllSubscriptions()
      await load()
      toast.push('全部订阅已刷新')
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy('')
    }
  }

  const testNode = async (node: MiproxyNode) => {
    const key = latencyKey(node)
    setTests((t) => ({ ...t, [key]: null }))
    try {
      const res = await api.TestProxyNode(node)
      setTests((t) => ({ ...t, [key]: res }))
      void api
        .SaveLatencyResults([
          { key, latencyMs: res.latencyMs ?? 0, ok: res.ok, error: res.error, at: new Date().toISOString() },
        ])
        .catch(() => {})
    } catch (err) {
      toast.error(err)
    }
  }

  const activateNode = async (node: MiproxyNode) => {
    setBusy('activate')
    try {
      const res = await api.ActivateProxyNode(node)
      await reloadSettings()
      setRuntime(await api.ProxyRuntimeStatus())
      const mode = res.mode === 'engine' ? '本地引擎' : '上游代理'
      toast.push(`已切换节点：${node.name}（${mode}）`)
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy('')
    }
  }

  const testAll = async (nodes: MiproxyNode[]) => {
    if (nodes.length === 0) return
    setBusy('testall')
    try {
      const results = await api.TestAllNodes(nodes)
      setTests((prev) => {
        const next = { ...prev }
        results.forEach((r) => {
          const node = nodes[r.index]
          if (node) next[latencyKey(node)] = { ok: r.ok, latencyMs: r.latencyMs, error: r.error }
        })
        return next
      })
      const ok = results.filter((r) => r.ok).length
      void api
        .SaveLatencyResults(
          results.map((r) => {
            const node = nodes[r.index]
            return {
              key: node ? latencyKey(node) : String(r.index),
              latencyMs: r.latencyMs ?? 0,
              ok: r.ok,
              error: r.error,
              at: new Date().toISOString(),
            }
          }),
        )
        .catch(() => {})
      toast.push(`测速完成：${ok}/${results.length} 个节点可用`)
    } catch (err) {
      toast.error(err)
    } finally {
      setBusy('')
    }
  }

  const deactivate = async () => {
    try {
      await api.DeactivateProxy()
      await reloadSettings()
      setRuntime(await api.ProxyRuntimeStatus())
      toast.push('已停止代理并恢复直连')
    } catch (err) {
      toast.error(err)
    }
  }

  const importLinks = async () => {
    try {
      const nodes = await api.ImportNodeLinks(importText)
      setImportText('')
      setImportOpen(false)
      await load()
      toast.push(`已导入 ${nodes.length} 个节点`)
    } catch (err) {
      toast.error(err)
    }
  }

  const stats = runtime?.stats
  const nodeRows = useMemo(() => nodes, [nodes])

  return (
    <div>
      {runtime?.running && (
        <div className="card" style={{ borderColor: 'var(--accent)' }}>
          <div className="row">
            <div style={{ flex: 1 }}>
              <h3 className="card-title" style={{ marginBottom: 2 }}>
                代理运行中
                <Tag kind="ok">{PROTOCOL_LABEL[runtime.node?.protocol ?? ''] ?? runtime.node?.protocol}</Tag>
                {runtime.external ? <Tag kind="warn">外部可用</Tag> : <Tag>仅本机</Tag>}
              </h3>
              <div className="small faint">
                {runtime.node?.name} · {runtime.node?.server}:{runtime.node?.port} → 本地{' '}
                <span className="mono">{runtime.addr}</span>
                {runtime.auth ? '（需认证）' : ''}
              </div>
              {stats ? (
                <div className="small faint" style={{ marginTop: 4 }}>
                  连接 {stats.accepted} · 活动 {stats.active} · 失败 {stats.failed} · 上行{' '}
                  {formatBytes(stats.bytesIn)} · 下行 {formatBytes(stats.bytesOut)} · 启动 {formatTime(runtime.since)}
                </div>
              ) : null}
            </div>
            <button className="btn btn-danger" onClick={deactivate}>
              停止并恢复直连
            </button>
          </div>
        </div>
      )}

      <div className="card">
        <div className="row">
          <div>
            <h3 className="card-title">代理订阅</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              支持 VMess / VLESS / Trojan / Shadowsocks / HTTP / SOCKS5 节点；内置 Go 代理引擎，选中节点即可启用。
            </p>
          </div>
          <div className="spacer" />
          <button
            className="btn"
            disabled={busy !== ''}
            onClick={async () => {
              setBusy('sync')
              try {
                const res = await api.SyncSubscriptions()
                await load()
                toast.push(res.message || '订阅已同步')
              } catch (err) {
                toast.error(err)
              } finally {
                setBusy('')
              }
            }}
          >
            同步订阅
          </button>
          <label className="row" style={{ gap: 6 }} title="通过当前节点拉取订阅">
            <Switch
              checked={useProxy}
              onChange={async (v) => {
                setUseProxy(v)
                try {
                  const cur = await api.GetSettings()
                  await api.SaveSettings({ ...cur, subscriptions: { ...(cur.subscriptions ?? {}), useProxy: v } })
                } catch (err) {
                  toast.error(err)
                }
              }}
            />
            <span className="small">订阅走代理</span>
          </label>
          <button className="btn" onClick={refreshAll} disabled={busy !== '' || list.length === 0}>
            全部刷新
          </button>
          <button className="btn" onClick={() => setImportOpen(true)}>
            导入节点链接
          </button>
          <button
            className="btn btn-primary"
            onClick={() =>
              setEditing({
                id: '',
                name: '',
                url: '',
                enabled: true,
                intervalHours: 12,
                nodes: [],
                info: { upload: 0, download: 0, total: 0, expire: 0 },
              })
            }
          >
            新建订阅
          </button>
        </div>
      </div>

      {loading ? (
        <div style={{ padding: 24 }}>
          <Spinner label="加载订阅" />
        </div>
      ) : list.length === 0 ? (
        <Empty icon="⇅" title="还没有订阅" hint="新建订阅或导入节点链接后即可使用" />
      ) : (
        <div className="grid-2" style={{ marginTop: 14 }}>
          {list.map((src) => {
            const used = src.info.download + src.info.upload
            const pct = src.info.total > 0 ? Math.min(100, (used / src.info.total) * 100) : 0
            return (
              <div key={src.id} className="scope-card">
                <div className="scope-head">
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div className="scope-title truncate">{src.name || '未命名订阅'}</div>
                    <div className="scope-desc truncate mono" title={src.url}>
                      {src.url}
                    </div>
                  </div>
                  {src.lastError ? <Tag kind="bad">异常</Tag> : <Tag kind="ok">{src.nodes.length} 节点</Tag>}
                  <Switch
                    checked={src.enabled}
                    onChange={async (v) => {
                      try {
                        await api.SaveSubscription({ ...src, enabled: v })
                        await load()
                      } catch (err) {
                        toast.error(err)
                      }
                    }}
                  />
                </div>
                {src.info.total > 0 && (
                  <div className="col" style={{ gap: 4 }}>
                    <Progress percent={pct} />
                    <div className="row small faint">
                      <span>
                        已用 {formatBytes(used)} / {formatBytes(src.info.total)}
                      </span>
                      <div className="spacer" />
                      {src.info.expire > 0 && <span>到期 {formatTime(src.info.expire * 1000)}</span>}
                    </div>
                  </div>
                )}
                <div className="row small faint">
                  <span>每 {src.intervalHours} 小时刷新</span>
                  <span>·</span>
                  <span>{src.lastFetch ? `上次 ${relativeTime(src.lastFetch)}` : '尚未刷新'}</span>
                </div>
                {src.lastError ? <div className="small" style={{ color: 'var(--danger)' }}>{src.lastError}</div> : null}
                <div className="row">
                  <button className="btn btn-sm" disabled={busy !== ''} onClick={() => refreshOne(src)}>
                    刷新
                  </button>
                  <button className="btn btn-sm" onClick={() => setEditing(src)}>
                    编辑
                  </button>
                  <button className="btn btn-sm" onClick={() => setSelected(src.id)}>
                    查看节点
                  </button>
                  <div className="spacer" />
                  <button className="btn btn-danger btn-sm" onClick={() => setDeleting(src)}>
                    删除
                  </button>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {current && (
        <div className="card" style={{ marginTop: 14 }}>
          <div className="row">
            <h3 className="card-title" style={{ margin: 0 }}>
              节点列表 · {current.name}
              <Tag>{nodeRows.length}</Tag>
            </h3>
            <div className="spacer" />
            <button className="btn btn-sm" disabled={busy === 'testall' || nodeRows.length === 0} onClick={() => testAll(nodeRows)}>
              {busy === 'testall' ? '测速中…' : '全部测速'}
            </button>
          </div>
          <p className="card-desc">点击「启用」启动本地代理引擎并把所选作用域切到该节点。</p>
          {nodeRows.length === 0 ? (
            <Empty icon="◇" title="该订阅没有可用节点" hint="刷新订阅，或检查链接格式" />
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>协议</th>
                  <th>服务器</th>
                  <th>传输</th>
                  <th className="right">延迟</th>
                  <th className="right">操作</th>
                </tr>
              </thead>
              <tbody>
                {nodeRows.map((node, index) => {
                  const key = latencyKey(node)
                  const result = tests[key]
                  const active = runtime?.running && runtime.node?.name === node.name && runtime.node?.server === node.server
                  return (
                    <tr key={key}>
                      <td className="truncate" title={node.name}>
                        {node.name}
                        {active ? <Tag kind="ok">使用中</Tag> : null}
                      </td>
                      <td>{protocolTag(node.protocol)}</td>
                      <td className="mono small">
                        {node.server}:{node.port}
                      </td>
                      <td className="small faint">
                        {(node.network ?? 'tcp').toUpperCase()}
                        {node.tls ? ' + TLS' : ''}
                      </td>
                      <td className="right mono small">
                        {result ? (result.ok ? `${result.latencyMs ?? 0} ms` : '失败') : '—'}
                      </td>
                      <td className="right">
                        <div className="row" style={{ justifyContent: 'flex-end', gap: 6 }}>
                          {result ? (
                            <span className={`tag ${result.ok ? 'tag-ok' : 'tag-bad'}`}>
                              {result.ok ? `${result.latencyMs ?? 0}ms` : '失败'}
                            </span>
                          ) : null}
                          <button className="btn btn-sm" onClick={() => testNode(node)}>
                            测速
                          </button>
                          <button className="btn btn-primary btn-sm" disabled={busy === 'activate'} onClick={() => activateNode(node)}>
                            启用
                          </button>
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      )}

      <div className="card">
        <div className="row">
          <h3 className="card-title" style={{ margin: 0 }}>
            自定义节点
            <Tag>{customNodes.length}</Tag>
          </h3>
          <div className="spacer" />
          <button
            className="btn btn-sm"
            disabled={busy === 'testall' || customNodes.length === 0}
            onClick={() => testAll(customNodes)}
          >
            {busy === 'testall' ? '测速中…' : '全部测速'}
          </button>
          <button
            className="btn btn-sm"
            onClick={() =>
              setNodeModal({
                name: '',
                protocol: 'vmess',
                server: '',
                port: 443,
                uuid: '',
                alterId: 0,
                security: 'auto',
                network: 'ws',
                path: '/',
                host: '',
                tls: false,
              })
            }
          >
            添加节点
          </button>
        </div>
        <p className="card-desc">手动添加或粘贴分享链接导入，支持与订阅节点同样的协议。</p>
        {customNodes.length === 0 ? (
          <Empty icon="＋" title="还没有自定义节点" hint="点击「添加节点」或「导入节点链接」" />
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>协议</th>
                <th>服务器</th>
                <th>传输</th>
                <th className="right">延迟</th>
                <th className="right">操作</th>
              </tr>
            </thead>
            <tbody>
              {customNodes.map((node, index) => {
                const key = latencyKey(node)
                const result = tests[key]
                const active = runtime?.running && runtime.node?.id !== '' && runtime.node?.id === node.id
                return (
                  <tr key={key}>
                    <td className="truncate" title={node.name}>
                      {node.name}
                      {active ? <Tag kind="ok">使用中</Tag> : null}
                    </td>
                    <td>{protocolTag(node.protocol)}</td>
                    <td className="mono small">
                      {node.server}:{node.port}
                    </td>
                    <td className="small faint">
                      {(node.network ?? 'tcp').toUpperCase()}
                      {node.tls ? ' + TLS' : ''}
                    </td>
                    <td className="right mono small">
                      {result ? (result.ok ? `${result.latencyMs ?? 0} ms` : '失败') : '—'}
                    </td>
                    <td className="right">
                      <div className="row" style={{ justifyContent: 'flex-end', gap: 6 }}>
                        {result ? (
                          <span className={`tag ${result.ok ? 'tag-ok' : 'tag-bad'}`}>
                            {result.ok ? `${result.latencyMs ?? 0}ms` : '失败'}
                          </span>
                        ) : null}
                        <button className="btn btn-sm" onClick={() => testNode(node)}>
                          测速
                        </button>
                        <button className="btn btn-sm" onClick={() => setNodeModal(node)}>
                          编辑
                        </button>
                        <button className="btn btn-primary btn-sm" disabled={busy === 'activate'} onClick={() => activateNode(node)}>
                          启用
                        </button>
                        <button
                          className="btn btn-danger btn-sm"
                          onClick={async () => {
                            try {
                              await api.DeleteCustomNode(node.id ?? '')
                              await load()
                              toast.push('已删除节点')
                            } catch (err) {
                              toast.error(err)
                            }
                          }}
                        >
                          删除
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>


      {editing && <SubscriptionEditor initial={editing} onCancel={() => setEditing(null)} onSave={saveEditor} />}

      {nodeModal && (
        <NodeEditor
          initial={nodeModal}
          onCancel={() => setNodeModal(null)}
          onSave={async (node) => {
            try {
              await api.SaveCustomNode(node)
              setNodeModal(null)
              await load()
              toast.push('节点已保存')
            } catch (err) {
              toast.error(err)
            }
          }}
        />
      )}

      {importOpen && (
        <Modal
          title="导入节点链接"
          wide
          onClose={() => setImportOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setImportOpen(false)}>
                取消
              </button>
              <button className="btn btn-primary" onClick={importLinks} disabled={!importText.trim()}>
                导入
              </button>
            </>
          }
        >
          <Field label="每行一个分享链接" hint="支持 vmess://、vless://、trojan://、ss://、http://、socks5://">
            <textarea
              className="textarea"
              style={{ minHeight: 180 }}
              value={importText}
              onChange={(e) => setImportText(e.target.value)}
              placeholder="vmess://eyJ2IjoiMiIsInBzIjoi..."
            />
          </Field>
        </Modal>
      )}

      {deleting && (
        <Confirm
          title="删除订阅"
          message={`确定删除「${deleting.name}」吗？已启用的节点不会被自动停用。`}
          danger
          onCancel={() => setDeleting(null)}
          onConfirm={async () => {
            try {
              await api.DeleteSubscription(deleting.id)
              setDeleting(null)
              await load()
              toast.push('订阅已删除')
            } catch (err) {
              toast.error(err)
            }
          }}
        />
      )}
    </div>
  )

  async function saveEditor(draft: SubscriptionSource) {
    try {
      await api.SaveSubscription(draft)
      setEditing(null)
      await load()
      toast.push('订阅已保存')
    } catch (err) {
      toast.error(err)
    }
  }
}

function latencyKey(node: MiproxyNode): string {
  return `${node.protocol}|${node.server}|${node.port}|${node.name}`
}

function nodeKey(node: MiproxyNode, index = 0): string {
  return `${node.protocol}|${node.server}|${node.port}|${node.name}|${node.id ?? ''}|${index}`
}

function SubscriptionEditor({
  initial,
  onCancel,
  onSave,
}: {
  initial: SubscriptionSource
  onCancel: () => void
  onSave: (src: SubscriptionSource) => void
}) {
  const [draft, setDraft] = useState<SubscriptionSource>({ ...initial })
  const patch = (next: Partial<SubscriptionSource>) => setDraft((cur) => ({ ...cur, ...next }))
  return (
    <Modal
      title={initial.id ? '编辑订阅' : '新建订阅'}
      onClose={onCancel}
      footer={
        <>
          <button className="btn" onClick={onCancel}>
            取消
          </button>
          <button className="btn btn-primary" onClick={() => onSave(draft)} disabled={!draft.url.trim()}>
            保存
          </button>
        </>
      }
    >
      <Field label="名称">
        <input className="input" value={draft.name} onChange={(e) => patch({ name: e.target.value })} placeholder="例如：机场 A" />
      </Field>
      <Field label="订阅地址" hint="支持 Base64 节点列表、明文链接、Clash YAML">
        <input
          className="input mono"
          value={draft.url}
          onChange={(e) => patch({ url: e.target.value })}
          placeholder="https://example.com/api/v1/client/subscribe?token=..."
        />
      </Field>
      <div className="row" style={{ gap: 12 }}>
        <Field label="刷新间隔（小时）">
          <input
            className="input"
            type="number"
            min={1}
            max={168}
            value={draft.intervalHours}
            onChange={(e) => patch({ intervalHours: Math.max(1, Number(e.target.value) || 12) })}
          />
        </Field>
        <Field label="启用自动刷新">
          <Switch checked={draft.enabled} onChange={(v) => patch({ enabled: v })} />
        </Field>
      </div>
    </Modal>
  )
}

function NodeEditor({
  initial,
  onCancel,
  onSave,
}: {
  initial: MiproxyNode
  onCancel: () => void
  onSave: (node: MiproxyNode) => void
}) {
  const [draft, setDraft] = useState<MiproxyNode>({ network: 'ws', path: '/', alterId: 0, security: 'auto', ...initial })
  const patch = (next: Partial<MiproxyNode>) => setDraft((cur) => ({ ...cur, ...next }))
  const needsUUID = draft.protocol === 'vmess' || draft.protocol === 'vless'
  const needsPassword = draft.protocol === 'trojan' || draft.protocol === 'shadowsocks'
  const needsMethod = draft.protocol === 'shadowsocks'
  return (
    <Modal
      title={initial.id ? '编辑节点' : '添加节点'}
      wide
      onClose={onCancel}
      footer={
        <>
          <button className="btn" onClick={onCancel}>
            取消
          </button>
          <button className="btn btn-primary" onClick={() => onSave(draft)} disabled={!draft.server || !draft.port}>
            保存
          </button>
        </>
      }
    >
      <div className="grid-2">
        <Field label="名称">
          <input className="input" value={draft.name} onChange={(e) => patch({ name: e.target.value })} />
        </Field>
        <Field label="协议">
          <select className="select" value={draft.protocol} onChange={(e) => patch({ protocol: e.target.value })}>
            <option value="vmess">VMess</option>
            <option value="vless">VLESS</option>
            <option value="trojan">Trojan</option>
            <option value="shadowsocks">Shadowsocks</option>
            <option value="socks5">SOCKS5</option>
            <option value="http">HTTP</option>
          </select>
        </Field>
        <Field label="服务器">
          <input className="input mono" value={draft.server} onChange={(e) => patch({ server: e.target.value })} placeholder="example.com" />
        </Field>
        <Field label="端口">
          <input
            className="input"
            type="number"
            value={draft.port}
            onChange={(e) => patch({ port: Number(e.target.value) || 0 })}
          />
        </Field>
        {needsUUID && (
          <Field label="UUID">
            <input className="input mono" value={draft.uuid ?? ''} onChange={(e) => patch({ uuid: e.target.value })} />
          </Field>
        )}
        {draft.protocol === 'vmess' && (
          <Field label="AlterID">
            <input
              className="input"
              type="number"
              value={draft.alterId ?? 0}
              onChange={(e) => patch({ alterId: Number(e.target.value) || 0 })}
            />
          </Field>
        )}
        {needsMethod && (
          <Field label="加密方式">
            <select className="select" value={draft.method ?? 'aes-256-gcm'} onChange={(e) => patch({ method: e.target.value })}>
              <option value="aes-128-gcm">aes-128-gcm</option>
              <option value="aes-256-gcm">aes-256-gcm</option>
              <option value="chacha20-ietf-poly1305">chacha20-ietf-poly1305</option>
            </select>
          </Field>
        )}
        {needsPassword && (
          <Field label="密码">
            <input
              className="input"
              type="password"
              value={draft.password ?? ''}
              onChange={(e) => patch({ password: e.target.value })}
            />
          </Field>
        )}
        <Field label="传输">
          <select className="select" value={draft.network ?? 'tcp'} onChange={(e) => patch({ network: e.target.value })}>
            <option value="tcp">TCP</option>
            <option value="ws">WebSocket</option>
          </select>
        </Field>
        <Field label="WS 路径">
          <input className="input mono" value={draft.path ?? ''} onChange={(e) => patch({ path: e.target.value })} placeholder="/" />
        </Field>
        <Field label="WS Host">
          <input className="input mono" value={draft.host ?? ''} onChange={(e) => patch({ host: e.target.value })} placeholder="cdn.example.com" />
        </Field>
        <Field label="TLS">
          <Switch checked={!!draft.tls} onChange={(v) => patch({ tls: v })} />
        </Field>
        <Field label="SNI（可选）">
          <input className="input mono" value={draft.sni ?? ''} onChange={(e) => patch({ sni: e.target.value })} />
        </Field>
      </div>
    </Modal>
  )
}






