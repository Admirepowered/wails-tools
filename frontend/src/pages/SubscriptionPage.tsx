import { useCallback, useEffect, useState } from 'react'
import { api, onEvent, type SubscriptionSource, type Settings, type TestResult } from '../lib/api'
import { Confirm, Empty, Field, Modal, Progress, Spinner, Switch, Tag, useToast } from '../components/ui'
import { formatBytes, formatTime, relativeTime } from '../lib/format'
import type { PageProps } from '../App'

const SCOPE_LABEL: Record<string, string> = {
  http: 'HTTP 下载',
  netdisk: '网盘接口',
  ssh: 'SSH / SFTP',
  ftp: 'FTP',
  sync: '同步服务',
}

export default function SubscriptionPage({ reloadSettings }: PageProps) {
  const toast = useToast()
  const [list, setList] = useState<SubscriptionSource[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<string>('')
  const [editing, setEditing] = useState<SubscriptionSource | null>(null)
  const [deleting, setDeleting] = useState<SubscriptionSource | null>(null)
  const [selected, setSelected] = useState<string>('')
  const [applyNode, setApplyNode] = useState<{ source: SubscriptionSource; index: number } | null>(null)
  const [applyScopes, setApplyScopes] = useState<string[]>(['http', 'netdisk'])
  const [applyAll, setApplyAll] = useState(false)
  const [tests, setTests] = useState<Record<string, TestResult | null>>({})

  const load = useCallback(async () => {
    try {
      const items = await api.ListSubscriptions()
      setList(items)
      setSelected((prev) => (prev && items.some((i) => i.id === prev) ? prev : items[0]?.id ?? ''))
    } catch (err) {
      toast.error(err)
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    const off = onEvent<{ source: SubscriptionSource }>('subscription:updated', () => {
      void load()
    })
    return () => off()
  }, [load])

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

  const save = async (draft: SubscriptionSource) => {
    try {
      await api.SaveSubscription(draft)
      setEditing(null)
      await load()
      toast.push('订阅已保存')
    } catch (err) {
      toast.error(err)
    }
  }

  const testNode = async (src: SubscriptionSource, index: number) => {
    const key = `${src.id}:${index}`
    setTests((t) => ({ ...t, [key]: null }))
    try {
      const result = await api.TestSubscriptionNode(src.id, index)
      setTests((t) => ({ ...t, [key]: result }))
    } catch (err) {
      toast.error(err)
    }
  }

  const doApply = async () => {
    if (!applyNode) return
    try {
      await api.ApplySubscriptionNode(applyNode.source.id, applyNode.index, applyScopes, applyAll)
      await reloadSettings()
      setApplyNode(null)
      toast.push('代理已应用')
    } catch (err) {
      toast.error(err)
    }
  }

  const current = list.find((s) => s.id === selected) ?? null

  return (
    <div>
      <div className="card">
        <div className="row">
          <div>
            <h3 className="card-title">代理订阅</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              订阅地址会自动拉取 HTTP / SOCKS5 节点，并按设定的间隔在后台刷新；选中节点后可一键应用到指定的连接类型。
            </p>
          </div>
          <div className="spacer" />
          <button className="btn" onClick={refreshAll} disabled={busy !== '' || list.length === 0}>
            全部刷新
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
        <Empty icon="⇅" title="还没有订阅" hint="点击右上角「新建订阅」，粘贴订阅链接即可导入节点" />
      ) : (
        <div className="grid-2" style={{ marginTop: 14 }}>
          {list.map((src) => {
            const used = src.info.download + src.info.upload
            const pct = src.info.total > 0 ? Math.min(100, (used / src.info.total) * 100) : 0
            return (
              <div key={src.id} className={`scope-card ${selected === src.id ? '' : ''}`}>
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

      {current && current.nodes.length > 0 && (
        <div className="card" style={{ marginTop: 14 }}>
          <h3 className="card-title">
            节点列表 · {current.name}
            <Tag>{current.nodes.length}</Tag>
          </h3>
          <p className="card-desc">点击「应用」把该节点写入代理设置，可选择作用域或全部连接。</p>
          <table className="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>协议</th>
                <th>地址</th>
                <th className="right">操作</th>
              </tr>
            </thead>
            <tbody>
              {current.nodes.map((node, index) => {
                const key = `${current.id}:${index}`
                const result = tests[key]
                return (
                  <tr key={key}>
                    <td className="truncate" title={node.name}>
                      {node.name}
                    </td>
                    <td>
                      <Tag kind={node.scheme === 'socks5' ? 'info' : 'default'}>
                        {node.scheme === 'socks5' ? 'SOCKS5' : 'HTTP'}
                      </Tag>
                    </td>
                    <td className="mono small">
                      {node.host}:{node.port}
                    </td>
                    <td className="right">
                      <div className="row" style={{ justifyContent: 'flex-end', gap: 6 }}>
                        {result ? (
                          <span className={`tag ${result.ok ? 'tag-ok' : 'tag-bad'}`}>
                            {result.ok ? `${result.latencyMs}ms` : '不可用'}
                          </span>
                        ) : null}
                        <button className="btn btn-sm" onClick={() => testNode(current, index)}>
                          测试
                        </button>
                        <button
                          className="btn btn-primary btn-sm"
                          onClick={() => {
                            setApplyNode({ source: current, index })
                            setApplyAll(false)
                          }}
                        >
                          应用
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {editing && <SubscriptionEditor initial={editing} onCancel={() => setEditing(null)} onSave={save} />}

      {applyNode && (
        <Modal
          title="应用代理节点"
          onClose={() => setApplyNode(null)}
          footer={
            <>
              <button className="btn" onClick={() => setApplyNode(null)}>
                取消
              </button>
              <button className="btn btn-primary" onClick={doApply}>
                应用
              </button>
            </>
          }
        >
          <div className="small dim">
            节点：{applyNode.source.nodes[applyNode.index]?.name}（
            {applyNode.source.nodes[applyNode.index]?.scheme}://{applyNode.source.nodes[applyNode.index]?.host}:
            {applyNode.source.nodes[applyNode.index]?.port}）
          </div>
          <label className="row" style={{ gap: 8 }}>
            <Switch checked={applyAll} onChange={setApplyAll} />
            <span>应用到全部连接类型</span>
          </label>
          {!applyAll && (
            <div className="col">
              {Object.keys(SCOPE_LABEL).map((scope) => (
                <label key={scope} className="row" style={{ gap: 8 }}>
                  <input
                    type="checkbox"
                    checked={applyScopes.includes(scope)}
                    onChange={(e) =>
                      setApplyScopes((prev) =>
                        e.target.checked ? [...new Set([...prev, scope])] : prev.filter((s) => s !== scope),
                      )
                    }
                  />
                  <span>{SCOPE_LABEL[scope]}</span>
                </label>
              ))}
            </div>
          )}
        </Modal>
      )}

      {deleting && (
        <Confirm
          title="删除订阅"
          message={`确定删除「${deleting.name}」吗？已应用的代理设置不会自动撤销。`}
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
      <Field label="订阅地址" hint="支持明文/Base64 节点列表、Clash YAML 与 JSON">
        <input
          className="input mono"
          value={draft.url}
          onChange={(e) => patch({ url: e.target.value })}
          placeholder="https://example.com/sub?token=..."
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
