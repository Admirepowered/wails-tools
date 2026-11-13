import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, type Note, type SyncResult } from '../lib/api'
import { Confirm, Empty, Spinner, Switch, Tag, useToast } from '../components/ui'
import { relativeTime } from '../lib/format'
import type { PageProps } from '../App'

const EMPTY_NOTE: Note = {
  id: '',
  title: '未命名笔记',
  content: '',
  folder: '',
  tags: [],
  pinned: false,
  deleted: false,
  updatedAt: 0,
  createdAt: 0,
  revision: 0,
}

export default function NotesPage({ serverStatus }: PageProps) {
  const toast = useToast()
  const [notes, setNotes] = useState<Note[]>([])
  const [folders, setFolders] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [query, setQuery] = useState('')
  const [folderFilter, setFolderFilter] = useState('')
  const [selectedId, setSelectedId] = useState('')
  const [draft, setDraft] = useState<Note | null>(null)
  const [tagText, setTagText] = useState('')
  const [preview, setPreview] = useState(false)
  const [saving, setSaving] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [confirming, setConfirming] = useState(false)

  const draftRef = useRef<Note | null>(null)
  const timerRef = useRef<number | null>(null)
  const pendingRef = useRef(false)

  const reloadList = useCallback(async (): Promise<Note[]> => {
    const [list, folderList] = await Promise.all([api.ListNotes(false), api.NoteFolders()])
    setNotes(list)
    setFolders(folderList)
    return list
  }, [])

  /** 把当前草稿写回后端；返回后端返回的笔记（失败时为 null）。 */
  const flush = useCallback(async (force = false): Promise<Note | null> => {
    const cur = draftRef.current
    if (!cur) return null
    if (!force && !pendingRef.current) return cur
    pendingRef.current = false
    setSaving(true)
    try {
      const saved = await api.SaveNote({ ...cur, tags: cur.tags ?? [] })
      const live = draftRef.current
      if (live === cur) {
        // 没有更新的输入，直接采用后端结果（拿到 id / revision / dirty）
        draftRef.current = saved
        setDraft(saved)
        setSelectedId(saved.id)
      } else if (live && !live.id && saved.id) {
        // 保存期间又有了新输入：只补齐服务端分配的 id，不覆盖内容
        const merged: Note = { ...live, id: saved.id, createdAt: saved.createdAt, revision: saved.revision }
        draftRef.current = merged
        setDraft(merged)
        setSelectedId(saved.id)
      }
      setNotes((prev) => [saved, ...prev.filter((n) => n.id !== saved.id && n.id !== cur.id)])
      return saved
    } catch (err) {
      pendingRef.current = true
      toast.error(err)
      return null
    } finally {
      setSaving(false)
    }
  }, [toast])

  // 800ms 防抖自动保存
  const schedule = useCallback(() => {
    pendingRef.current = true
    if (timerRef.current !== null) window.clearTimeout(timerRef.current)
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null
      void flush()
    }, 800)
  }, [flush])

  const patch = useCallback(
    (p: Partial<Note>) => {
      const cur = draftRef.current
      if (!cur) return
      const next: Note = { ...cur, ...p }
      draftRef.current = next
      setDraft(next)
      schedule()
    },
    [schedule],
  )

  const select = useCallback(
    (note: Note) => {
      const live = draftRef.current
      // 点击的正是当前编辑的笔记：保留编辑器里的内容（可能还有待保存的改动）
      if (live && live.id && live.id === note.id) return
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
        timerRef.current = null
      }
      void flush()
      draftRef.current = note
      setDraft(note)
      setSelectedId(note.id)
      setPreview(false)
    },
    [flush],
  )

  useEffect(() => {
    let alive = true
    void (async () => {
      try {
        await reloadList()
      } catch (err) {
        toast.error(err)
      } finally {
        if (alive) setLoading(false)
      }
    })()
    return () => {
      alive = false
    }
  }, [reloadList, toast])

  // 切换笔记时同步标签输入框
  const draftId = draft?.id ?? ''
  useEffect(() => {
    setTagText((draftRef.current?.tags ?? []).join(', '))
  }, [draftId])

  // 组件卸载时补一次保存，避免丢改动
  useEffect(() => {
    return () => {
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
        timerRef.current = null
      }
      void flush()
    }
  }, [flush])

  const createNote = useCallback(async () => {
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current)
      timerRef.current = null
    }
    try {
      await flush()
      const saved = await api.SaveNote({
        id: '',
        title: '未命名笔记',
        content: '',
        folder: '',
        tags: [],
        pinned: false,
        deleted: false,
        updatedAt: 0,
        createdAt: 0,
        revision: 0,
      })
      setNotes((prev) => [saved, ...prev.filter((n) => n.id !== saved.id)])
      draftRef.current = saved
      setDraft(saved)
      setSelectedId(saved.id)
      setPreview(false)
      setQuery('')
      setFolderFilter('')
      try {
        setFolders(await api.NoteFolders())
      } catch (err) {
        toast.error(err)
      }
    } catch (err) {
      toast.error(err)
    }
  }, [flush, toast])

  const removeNote = useCallback(async () => {
    setConfirming(false)
    const cur = draftRef.current
    if (!cur) return
    if (!cur.id) {
      draftRef.current = null
      setDraft(null)
      setSelectedId('')
      return
    }
    try {
      await api.DeleteNote(cur.id)
      setNotes((prev) => prev.filter((n) => n.id !== cur.id))
      draftRef.current = null
      setDraft(null)
      setSelectedId('')
      setTagText('')
      toast.push('笔记已删除')
      try {
        await reloadList()
      } catch (err) {
        toast.error(err)
      }
    } catch (err) {
      toast.error(err)
    }
  }, [reloadList, toast])

  const saveNow = useCallback(async () => {
    const saved = await flush(true)
    if (saved) toast.push('已保存到本地')
  }, [flush, toast])

  const exportMd = useCallback(async () => {
    const saved = await flush()
    if (!saved?.id) return
    try {
      const target = await api.ExportNoteMarkdown(saved.id, '')
      toast.push(`已导出到 ${target}`)
    } catch (err) {
      toast.error(err)
    }
  }, [flush, toast])

  const sync = useCallback(async () => {
    setSyncing(true)
    try {
      await flush()
      const res: SyncResult = await api.SyncNotes()
      toast.push(`${res.message || '同步完成'} · 推送 ${res.pushed} / 拉取 ${res.pulled}`)
      const list = await reloadList()
      const live = draftRef.current
      if (live?.id) {
        const fresh = list.find((n) => n.id === live.id)
        if (fresh) {
          draftRef.current = fresh
          setDraft(fresh)
        } else {
          draftRef.current = null
          setDraft(null)
          setSelectedId('')
        }
      }
    } catch (err) {
      toast.error(err)
    } finally {
      setSyncing(false)
    }
  }, [flush, reloadList, toast])

  const folderOptions = useMemo(() => {
    const set = new Set<string>(folders)
    for (const n of notes) if (n.folder) set.add(n.folder)
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [folders, notes])

  const visibleNotes = useMemo(() => {
    const q = query.trim().toLowerCase()
    const list = notes.filter((n) => {
      if (folderFilter && (n.folder ?? '') !== folderFilter) return false
      if (!q) return true
      if (n.title.toLowerCase().includes(q)) return true
      if (n.content.toLowerCase().includes(q)) return true
      return (n.tags ?? []).some((t) => t.toLowerCase().includes(q))
    })
    return [...list].sort((a, b) => {
      if (a.pinned !== b.pinned) return a.pinned ? -1 : 1
      return b.updatedAt - a.updatedAt
    })
  }, [notes, query, folderFilter])

  const dirtyCount = notes.filter((n) => n.dirty).length

  return (
    <>
      <div className="split split-wide">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, minHeight: 0 }}>
          <button className="btn btn-primary" onClick={createNote}>
            ＋ 新建笔记
          </button>
          <input
            className="input"
            placeholder="搜索标题 / 内容 / 标签"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <select className="select" value={folderFilter} onChange={(e) => setFolderFilter(e.target.value)}>
            <option value="">全部文件夹</option>
            {folderOptions.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>
          <div className="row small faint" style={{ gap: 6 }}>
            <span>
              {visibleNotes.length} 篇{dirtyCount > 0 ? ` · ${dirtyCount} 篇待同步` : ''}
            </span>
            <div className="spacer" />
            {serverStatus?.connected ? <Tag kind="ok">服务器已连接</Tag> : <Tag>服务器未连接</Tag>}
          </div>

          <div style={{ overflow: 'auto', minHeight: 0, display: 'flex', flexDirection: 'column', gap: 2 }}>
            {loading ? (
              <div style={{ padding: 16 }}>
                <Spinner label="加载笔记" />
              </div>
            ) : visibleNotes.length === 0 ? (
              <Empty icon="✎" title={notes.length === 0 ? '还没有笔记' : '没有匹配的笔记'} hint={notes.length === 0 ? '点击上方「新建笔记」开始记录' : '试试换个关键词或文件夹'} />
            ) : (
              visibleNotes.map((n) => (
                <div
                  key={n.id}
                  className={`file-row ${selectedId === n.id ? 'selected' : ''}`}
                  style={{ flexDirection: 'column', alignItems: 'stretch', gap: 2 }}
                  title={n.title || '未命名笔记'}
                  onClick={() => select(n)}
                >
                  <div className="row" style={{ gap: 6 }}>
                    {n.pinned ? (
                      <span className="small" style={{ color: 'var(--accent)' }}>
                        ★
                      </span>
                    ) : null}
                    <span className="file-name" style={{ fontWeight: 560 }}>
                      {n.title || '未命名笔记'}
                    </span>
                    {n.dirty ? (
                      <span
                        title="未同步"
                        style={{
                          width: 6,
                          height: 6,
                          borderRadius: '50%',
                          background: 'var(--warn)',
                          display: 'inline-block',
                          flex: '0 0 auto',
                        }}
                      />
                    ) : null}
                  </div>
                  <div className="small faint truncate">{snippet(n.content)}</div>
                  <div className="small faint" style={{ fontSize: 11 }}>
                    {relativeTime(n.updatedAt)}
                    {n.folder ? ` · ${n.folder}` : ''}
                  </div>
                </div>
              ))
            )}
          </div>
        </div>

        <div className="card pane" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {draft ? (
            <>
              <input
                className="input"
                style={{
                  fontSize: 18,
                  fontWeight: 620,
                  background: 'transparent',
                  border: 'none',
                  padding: '2px 0',
                }}
                placeholder="未命名笔记"
                value={draft.title}
                onChange={(e) => patch({ title: e.target.value })}
              />

              <div className="row row-wrap" style={{ gap: 8 }}>
                <input
                  className="input mono"
                  style={{ width: 150 }}
                  placeholder="文件夹"
                  value={draft.folder ?? ''}
                  onChange={(e) => patch({ folder: e.target.value })}
                />
                <input
                  className="input"
                  style={{ flex: 1, minWidth: 180 }}
                  placeholder="标签，用英文逗号分隔"
                  value={tagText}
                  onChange={(e) => {
                    const value = e.target.value
                    setTagText(value)
                    patch({
                      tags: value
                        .split(',')
                        .map((s) => s.trim())
                        .filter(Boolean),
                    })
                  }}
                />
                <span className="small dim nowrap">置顶</span>
                <Switch checked={draft.pinned} onChange={(v) => patch({ pinned: v })} title="置顶笔记" />
                {draft.dirty ? <Tag kind="warn">未同步</Tag> : <Tag kind="ok">已同步</Tag>}
              </div>

              <div className="row row-wrap" style={{ gap: 8 }}>
                <button className="btn btn-primary btn-sm" onClick={saveNow} disabled={saving}>
                  保存
                </button>
                <button className="btn btn-sm" onClick={exportMd} disabled={!draft.id || saving}>
                  导出 Markdown
                </button>
                <button className="btn btn-sm" onClick={sync} disabled={syncing}>
                  {syncing ? '同步中…' : '同步'}
                </button>
                <button className="btn btn-danger btn-sm" onClick={() => setConfirming(true)}>
                  删除
                </button>
                <div className="spacer" />
                {saving ? (
                  <span className="small faint">保存中…</span>
                ) : (
                  <span className="small faint">
                    {draft.content.length} 字符 · 更新于 {relativeTime(draft.updatedAt) || '—'}
                  </span>
                )}
                <div className="tabs" style={{ marginBottom: 0, borderBottom: 'none' }}>
                  <button className={`tab ${preview ? '' : 'active'}`} onClick={() => setPreview(false)}>
                    编辑
                  </button>
                  <button className={`tab ${preview ? 'active' : ''}`} onClick={() => setPreview(true)}>
                    预览
                  </button>
                </div>
              </div>

              <div className="divider" style={{ margin: '2px 0' }} />

              {preview ? (
                <div className="pane" style={{ minHeight: 380 }}>
                  {draft.content.trim() ? (
                    <Markdown source={draft.content} />
                  ) : (
                    <Empty icon="▤" title="暂无内容" hint="切回「编辑」开始书写" />
                  )}
                </div>
              ) : (
                <textarea
                  className="textarea"
                  style={{ minHeight: 380, fontFamily: 'var(--mono)' }}
                  placeholder="使用 Markdown 书写，支持标题、列表、引用、代码块、链接…"
                  value={draft.content}
                  onChange={(e) => patch({ content: e.target.value })}
                />
              )}
            </>
          ) : (
            <Empty icon="✎" title="从左侧选择或新建笔记" hint="笔记会自动保存，连接服务器后可双向同步" />
          )}
        </div>
      </div>

      {confirming ? (
        <Confirm
          title="删除笔记"
          message={`确定删除「${draft?.title || '未命名笔记'}」吗？本地会先标记删除，下次同步时同步到云端。`}
          danger
          onCancel={() => setConfirming(false)}
          onConfirm={removeNote}
        />
      ) : null}
    </>
  )
}

function snippet(content: string): string {
  const text = content.replace(/\s+/g, ' ').trim()
  if (!text) return '（空白笔记）'
  return text.length > 60 ? `${text.slice(0, 60)}…` : text
}

/* ------------------------------------------------------------------ *
 * 极简 Markdown 渲染：只生成 React 元素，文本节点由 React 转义，
 * 不使用 dangerouslySetInnerHTML。支持标题 / 列表 / 引用 / 代码块 /
 * 行内代码 / 粗体 / 斜体 / 链接 / 分隔线 / 段落。
 * ------------------------------------------------------------------ */

function Markdown({ source }: { source: string }) {
  const blocks = useMemo(() => renderBlocks(source), [source])
  return <div style={{ fontSize: 13.5, lineHeight: 1.72, wordBreak: 'break-word' }}>{blocks}</div>
}

function renderBlocks(source: string): ReactNode[] {
  const lines = source.replace(/\r\n?/g, '\n').split('\n')
  const out: ReactNode[] = []
  let i = 0
  let seq = 0

  while (i < lines.length) {
    const line = lines[i]
    const key = `b${seq++}`

    const fence = /^\s{0,3}```(\w*)\s*$/.exec(line)
    if (fence) {
      const buf: string[] = []
      i++
      while (i < lines.length && !/^\s{0,3}```\s*$/.test(lines[i])) {
        buf.push(lines[i])
        i++
      }
      i++
      out.push(
        <pre
          key={key}
          className="mono"
          style={{
            background: 'var(--bg-sunken)',
            border: '1px solid var(--border)',
            borderRadius: 8,
            padding: '10px 12px',
            margin: '10px 0',
            overflowX: 'auto',
            whiteSpace: 'pre',
          }}
        >
          <code>{buf.join('\n')}</code>
        </pre>,
      )
      continue
    }

    if (!line.trim()) {
      i++
      continue
    }

    if (/^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$/.test(line)) {
      out.push(<hr key={key} style={{ border: 'none', borderTop: '1px solid var(--border)', margin: '16px 0' }} />)
      i++
      continue
    }

    const heading = /^(#{1,6})\s+(.*)$/.exec(line)
    if (heading) {
      out.push(renderHeading(heading[1].length, mdInline(heading[2].trim(), key), key))
      i++
      continue
    }

    if (/^\s{0,3}>\s?/.test(line)) {
      const quote: string[] = []
      while (i < lines.length && /^\s{0,3}>\s?/.test(lines[i])) {
        quote.push(lines[i].replace(/^\s{0,3}>\s?/, ''))
        i++
      }
      out.push(
        <blockquote
          key={key}
          style={{
            margin: '10px 0',
            padding: '6px 12px',
            borderLeft: '3px solid var(--border-strong)',
            color: 'var(--text-dim)',
            background: 'var(--bg-sunken)',
            borderRadius: '0 8px 8px 0',
          }}
        >
          {quote.map((q, qi) => (
            <div key={`${key}-q${qi}`}>{mdInline(q, `${key}-q${qi}`)}</div>
          ))}
        </blockquote>,
      )
      continue
    }

    if (/^\s*[-*+]\s+/.test(line)) {
      const items: string[] = []
      while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*[-*+]\s+/, ''))
        i++
      }
      out.push(
        <ul key={key} style={{ margin: '8px 0', paddingLeft: 22 }}>
          {items.map((item, ii) => (
            <li key={`${key}-l${ii}`} style={{ margin: '3px 0' }}>
              {mdInline(item, `${key}-l${ii}`)}
            </li>
          ))}
        </ul>,
      )
      continue
    }

    if (/^\s*\d+[.)]\s+/.test(line)) {
      const items: string[] = []
      while (i < lines.length && /^\s*\d+[.)]\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*\d+[.)]\s+/, ''))
        i++
      }
      out.push(
        <ol key={key} style={{ margin: '8px 0', paddingLeft: 22 }}>
          {items.map((item, ii) => (
            <li key={`${key}-o${ii}`} style={{ margin: '3px 0' }}>
              {mdInline(item, `${key}-o${ii}`)}
            </li>
          ))}
        </ol>,
      )
      continue
    }

    const para: string[] = []
    while (i < lines.length && lines[i].trim() && !isBlockStart(lines[i])) {
      para.push(lines[i].trim())
      i++
    }
    if (para.length === 0) {
      // 兜底：理论上已在上面各分支处理，这里保证一定前进，避免死循环
      para.push(line.trim())
      i++
    }
    out.push(
      <p key={key} style={{ margin: '8px 0' }}>
        {mdInline(para.join('\n'), key)}
      </p>,
    )
  }

  return out
}

function isBlockStart(line: string): boolean {
  return (
    /^\s{0,3}```/.test(line) ||
    /^(#{1,6})\s+/.test(line) ||
    /^\s{0,3}>\s?/.test(line) ||
    /^\s*[-*+]\s+/.test(line) ||
    /^\s*\d+[.)]\s+/.test(line) ||
    /^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$/.test(line)
  )
}

function renderHeading(level: number, children: ReactNode, key: string): ReactNode {
  const base = { margin: '16px 0 8px', fontWeight: 650, lineHeight: 1.35 }
  switch (level) {
    case 1:
      return (
        <h1 key={key} style={{ ...base, fontSize: 22 }}>
          {children}
        </h1>
      )
    case 2:
      return (
        <h2 key={key} style={{ ...base, fontSize: 19 }}>
          {children}
        </h2>
      )
    case 3:
      return (
        <h3 key={key} style={{ ...base, fontSize: 17 }}>
          {children}
        </h3>
      )
    case 4:
      return (
        <h4 key={key} style={{ ...base, fontSize: 15 }}>
          {children}
        </h4>
      )
    case 5:
      return (
        <h5 key={key} style={{ ...base, fontSize: 14 }}>
          {children}
        </h5>
      )
    default:
      return (
        <h6 key={key} style={{ ...base, fontSize: 13 }}>
          {children}
        </h6>
      )
  }
}

function mdInline(text: string, prefix: string): ReactNode[] {
  const nodes: ReactNode[] = []
  const pattern = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\n]+\*)|(_[^_\n]+_)|(\[[^\]]+\]\([^)\s]+\))/
  let last = 0
  let index = 0
  let match: RegExpExecArray | null

  while ((match = pattern.exec(text)) !== null) {
    if (match.index > last) nodes.push(text.slice(last, match.index))
    const token = match[0]
    const key = `${prefix}-i${index++}`
    if (token.startsWith('`')) {
      nodes.push(
        <code
          key={key}
          className="mono"
          style={{
            background: 'var(--bg-sunken)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '0 4px',
          }}
        >
          {token.slice(1, -1)}
        </code>,
      )
    } else if (token.startsWith('**')) {
      nodes.push(<strong key={key}>{mdInline(token.slice(2, -2), key)}</strong>)
    } else if (token.startsWith('*') || token.startsWith('_')) {
      nodes.push(<em key={key}>{mdInline(token.slice(1, -1), key)}</em>)
    } else {
      const link = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(token)
      if (link) {
        nodes.push(
          <a key={key} href={link[2]} target="_blank" rel="noreferrer" style={{ color: 'var(--info)' }}>
            {link[1]}
          </a>,
        )
      } else {
        nodes.push(token)
      }
    }
    last = match.index + token.length
  }

  if (last < text.length) nodes.push(text.slice(last))
  return nodes
}
