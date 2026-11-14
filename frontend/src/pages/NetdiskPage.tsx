import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  api,
  onEvent,
  type NetdiskAccount,
  type NetdiskFile,
  type NetdiskFileList,
  type NetdiskProvider,
} from '../lib/api'
import { Confirm, Empty, Field, Modal, Progress, Spinner, Tabs, Tag, useToast } from '../components/ui'
import { formatBytes, formatTime, percent } from '../lib/format'
import type { PageProps } from '../App'

type NdTab = 'files' | 'shares' | 'recycle'

const FILE_PAGE_SIZE = 50
const SHARE_PAGE_SIZE = 20

const PROVIDER_FALLBACK: Record<string, string> = { quark: '夸克', uc: 'UC', baidu: '百度' }

interface ShareEntry {
  key: string
  shareId: string
  title: string
  url: string
  password: string
  fileCount: number
  viewCount: number
  createdAt: string
  expiredAt: string
}

interface RecycleEntry {
  key: string
  itemId: string
  name: string
  path: string
  isDir: boolean
  size: number
  deletedAt: string
}

interface UploadProgress {
  key: string
  done: number
  total: number
}

/* ---------------- defensive narrowing helpers ---------------- */

const LIST_KEYS = ['shares', 'items', 'list', 'files', 'records', 'rows', 'data', 'result']

function asRecord(value: unknown): Record<string, unknown> | null {
  if (value && typeof value === 'object' && !Array.isArray(value)) return value as Record<string, unknown>
  return null
}

function readString(rec: Record<string, unknown>, keys: string[]): string {
  for (const key of keys) {
    const v = rec[key]
    if (typeof v === 'string' && v !== '') return v
  }
  return ''
}

function readNumber(rec: Record<string, unknown>, keys: string[]): number {
  for (const key of keys) {
    const v = rec[key]
    if (typeof v === 'number' && Number.isFinite(v)) return v
    if (typeof v === 'string' && v !== '' && Number.isFinite(Number(v))) return Number(v)
  }
  return 0
}

function readBoolean(rec: Record<string, unknown>, keys: string[]): boolean {
  for (const key of keys) {
    const v = rec[key]
    if (typeof v === 'boolean') return v
  }
  return false
}

/** Finds the first array of rows inside a loosely typed backend response. */
function extractRows(raw: unknown): { rows: unknown[]; detected: boolean } {
  if (Array.isArray(raw)) return { rows: raw, detected: true }
  const rec = asRecord(raw)
  if (!rec) return { rows: [], detected: false }
  for (const key of LIST_KEYS) {
    const value = rec[key]
    if (Array.isArray(value)) return { rows: value, detected: true }
    const nested = asRecord(value)
    if (nested) {
      for (const inner of LIST_KEYS) {
        const v2 = nested[inner]
        if (Array.isArray(v2)) return { rows: v2, detected: true }
      }
    }
  }
  // Nothing recognised: fall back to the largest array found at the top level.
  let best: unknown[] | null = null
  for (const value of Object.values(rec)) {
    if (Array.isArray(value) && (best === null || value.length > best.length)) best = value
  }
  return { rows: best ?? [], detected: best !== null }
}

function extractHasMore(raw: unknown): boolean {
  const rec = asRecord(raw)
  if (!rec) return false
  if (readBoolean(rec, ['has_more', 'hasMore', 'more', 'hasNext'])) return true
  const next = asRecord(rec.next)
  if (next) return readBoolean(next, ['has_more', 'hasMore', 'more', 'hasNext'])
  return false
}

function extractRowsCount(raw: unknown): number {
  const rec = asRecord(raw)
  return rec ? readNumber(rec, ['total', 'count', 'total_count']) : 0
}

function normalizeShares(raw: unknown): ShareEntry[] {
  const { rows } = extractRows(raw)
  const out: ShareEntry[] = []
  rows.forEach((row, index) => {
    const rec = asRecord(row)
    if (!rec) return
    const shareId = readString(rec, ['share_id', 'shareId', 'shareid', 'id'])
    const title = readString(rec, ['title', 'name', 'share_name'])
    const url = readString(rec, ['url', 'share_url', 'shareUrl', 'link'])
    const password = readString(rec, ['password', 'passcode', 'pwd', 'pass'])
    const fileCount = readNumber(rec, ['file_count', 'fileCount', 'files_count'])
    const viewCount = readNumber(rec, ['view_count', 'viewCount', 'views'])
    const createdAt = readString(rec, ['created_at', 'createdAt', 'create_time'])
    const expiredAt = readString(rec, ['expired_at', 'expiredAt', 'expire_time'])
    if (!shareId && !title && !url) return
    out.push({
      key: `${shareId || url || title}-${index}`,
      shareId,
      title: title || shareId || `分享 ${index + 1}`,
      url,
      password,
      fileCount,
      viewCount,
      createdAt,
      expiredAt,
    })
  })
  return out
}

function normalizeRecycle(raw: unknown): RecycleEntry[] {
  const { rows } = extractRows(raw)
  const out: RecycleEntry[] = []
  rows.forEach((row, index) => {
    const rec = asRecord(row)
    if (!rec) return
    const itemId = readString(rec, ['operation_id', 'operationId', 'id', 'record_id', 'fid'])
    const name = readString(rec, ['name', 'file_name', 'fileName', 'title'])
    if (!itemId && !name) return
    out.push({
      key: `${itemId || name}-${index}`,
      itemId,
      name: name || itemId || `文件 ${index + 1}`,
      path: readString(rec, ['path', 'file_path']),
      isDir: readBoolean(rec, ['is_dir', 'isDir', 'dir']),
      size: readNumber(rec, ['size', 'file_size']),
      deletedAt: readString(rec, ['deleted_at', 'deletedAt', 'delete_time', 'created_at']),
    })
  })
  return out
}

async function copyText(text: string): Promise<boolean> {
  try {
    const rt = window.runtime
    if (rt?.ClipboardSetText) {
      await rt.ClipboardSetText(text)
      return true
    }
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    return false
  }
  return false
}

/* ---------------- page ---------------- */

export default function NetdiskPage(_props: PageProps) {
  const toast = useToast()

  const [providers, setProviders] = useState<NetdiskProvider[]>([])
  const [accounts, setAccounts] = useState<NetdiskAccount[]>([])
  const [accountsLoading, setAccountsLoading] = useState(true)
  const [activeId, setActiveId] = useState('')
  const [tab, setTab] = useState<NdTab>('files')
  const [refreshingId, setRefreshingId] = useState('')

  // file browser
  const [path, setPath] = useState('')
  const [fileList, setFileList] = useState<NetdiskFileList | null>(null)
  const [filesLoading, setFilesLoading] = useState(false)
  const [page, setPage] = useState(1)
  const [searchInput, setSearchInput] = useState('')
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [menu, setMenu] = useState<{ x: number; y: number; items: { label: string; danger?: boolean; action: () => void }[] } | null>(null)
  const [uploads, setUploads] = useState<UploadProgress[]>([])

  // shares
  const [sharePage, setSharePage] = useState(1)
  const [shareRows, setShareRows] = useState<ShareEntry[]>([])
  const [shareHasMore, setShareHasMore] = useState(false)
  const [shareDetected, setShareDetected] = useState(true)
  const [shareRaw, setShareRaw] = useState<unknown>(null)
  const [shareLoading, setShareLoading] = useState(false)

  // recycle bin
  const [recyclePage, setRecyclePage] = useState(1)
  const [recycleRows, setRecycleRows] = useState<RecycleEntry[]>([])
  const [recycleHasMore, setRecycleHasMore] = useState(false)
  const [recycleDetected, setRecycleDetected] = useState(true)
  const [recycleRaw, setRecycleRaw] = useState<unknown>(null)
  const [recycleLoading, setRecycleLoading] = useState(false)

  // dialogs
  const [addOpen, setAddOpen] = useState(false)
  const [addProvider, setAddProvider] = useState('')
  const [addCookie, setAddCookie] = useState('')
  const [addName, setAddName] = useState('')
  const [adding, setAdding] = useState(false)

  const [cookieFor, setCookieFor] = useState<NetdiskAccount | null>(null)
  const [cookieText, setCookieText] = useState('')
  const [cookieBusy, setCookieBusy] = useState(false)

  const [remarkFor, setRemarkFor] = useState<NetdiskAccount | null>(null)
  const [remarkText, setRemarkText] = useState('')
  const [remarkBusy, setRemarkBusy] = useState(false)

  const [removeFor, setRemoveFor] = useState<NetdiskAccount | null>(null)

  const [mkdirOpen, setMkdirOpen] = useState(false)
  const [mkdirName, setMkdirName] = useState('')
  const [mkdirBusy, setMkdirBusy] = useState(false)

  const [renameTarget, setRenameTarget] = useState<NetdiskFile | null>(null)
  const [renameName, setRenameName] = useState('')
  const [renameBusy, setRenameBusy] = useState(false)

  const [shareOpen, setShareOpen] = useState(false)
  const [sharePassword, setSharePassword] = useState('')
  const [shareExpire, setShareExpire] = useState(7)
  const [shareBusy, setShareBusy] = useState(false)
  const [shareResult, setShareResult] = useState<{ url: string; password: string } | null>(null)

  const [deleteTargets, setDeleteTargets] = useState<NetdiskFile[] | null>(null)
  const [recyclePurge, setRecyclePurge] = useState<RecycleEntry | null>(null)

  const providerName = useCallback(
    (id: string) => {
      const hit = providers.find((p) => p.id === id)
      return hit?.name || PROVIDER_FALLBACK[id] || id || '未知'
    },
    [providers],
  )

  const activeAccount = useMemo(() => accounts.find((a) => a.id === activeId) ?? null, [accounts, activeId])
  const files = useMemo<NetdiskFile[]>(() => (fileList?.files ? fileList.files : []), [fileList])
  const selectedFiles = useMemo(() => files.filter((f) => selected.includes(f.id)), [files, selected])
  const singleFile = selectedFiles.length === 1 && !selectedFiles[0].is_dir ? selectedFiles[0] : null
  const hasMore = fileList?.has_more === true

  const segments = useMemo(() => {
    const parts = path.split('/').filter(Boolean)
    let acc = ''
    return parts.map((part) => {
      acc += `/${part}`
      return { label: part, path: acc }
    })
  }, [path])

  /* ---------------- loading ---------------- */

  const loadAccounts = useCallback(async () => {
    try {
      const [providerList, accountList] = await Promise.all([api.NetdiskProviders(), api.ListNetdiskAccounts()])
      setProviders(providerList)
      setAccounts(accountList)
      setActiveId((prev) => (prev && accountList.some((a) => a.id === prev) ? prev : accountList.length > 0 ? accountList[0].id : ''))
      setAddProvider((prev) => prev || (providerList.length > 0 ? providerList[0].id : ''))
    } catch (err) {
      toast.error(err)
    } finally {
      setAccountsLoading(false)
    }
  }, [toast])

  const loadFiles = useCallback(
    async (accountId: string, targetPath: string, pageNum: number, searchQuery: string) => {
      if (!accountId) return
      setFilesLoading(true)
      try {
        const res = searchQuery
          ? await api.NetdiskSearch({
              account_id: accountId,
              path: targetPath || undefined,
              query: searchQuery,
              page: pageNum,
              page_size: FILE_PAGE_SIZE,
            })
          : await api.NetdiskListFiles({
              account_id: accountId,
              path: targetPath || undefined,
              page: pageNum,
              page_size: FILE_PAGE_SIZE,
            })
        setFileList(res)
        if (!searchQuery && pageNum === 1 && typeof res.path === 'string' && res.path) {
          setPath(res.path)
        }
        if (typeof res.page === 'number' && res.page > 0) setPage(res.page)
      } catch (err) {
        toast.error(err)
        setFileList(null)
      } finally {
        setFilesLoading(false)
      }
    },
    [toast],
  )

  const loadShares = useCallback(
    async (accountId: string, pageNum: number) => {
      if (!accountId) return
      setShareLoading(true)
      try {
        const raw = await api.NetdiskShares(accountId, pageNum, SHARE_PAGE_SIZE)
        const { detected } = extractRows(raw)
        setShareRaw(raw)
        setShareDetected(detected)
        setShareRows(normalizeShares(raw))
        setShareHasMore(extractHasMore(raw) || extractRowsCount(raw) > pageNum * SHARE_PAGE_SIZE)
      } catch (err) {
        toast.error(err)
        setShareRows([])
        setShareRaw(null)
        setShareHasMore(false)
      } finally {
        setShareLoading(false)
      }
    },
    [toast],
  )

  const loadRecycle = useCallback(
    async (accountId: string, pageNum: number) => {
      if (!accountId) return
      setRecycleLoading(true)
      try {
        const raw = await api.NetdiskRecycle(accountId, pageNum, SHARE_PAGE_SIZE)
        const { detected } = extractRows(raw)
        setRecycleRaw(raw)
        setRecycleDetected(detected)
        setRecycleRows(normalizeRecycle(raw))
        setRecycleHasMore(extractHasMore(raw) || extractRowsCount(raw) > pageNum * SHARE_PAGE_SIZE)
      } catch (err) {
        toast.error(err)
        setRecycleRows([])
        setRecycleRaw(null)
        setRecycleHasMore(false)
      } finally {
        setRecycleLoading(false)
      }
    },
    [toast],
  )

  useEffect(() => {
    void loadAccounts()
  }, [loadAccounts])

  useEffect(() => {
    if (!activeId) {
      setFileList(null)
      return
    }
    setPath('')
    setPage(1)
    setQuery('')
    setSearchInput('')
    setSelected([])
    setSharePage(1)
    setRecyclePage(1)
    void loadFiles(activeId, '', 1, '')
  }, [activeId, loadFiles])

  useEffect(() => {
    if (tab !== 'shares' || !activeId) return
    void loadShares(activeId, sharePage)
  }, [tab, activeId, sharePage, loadShares])

  useEffect(() => {
    if (tab !== 'recycle' || !activeId) return
    void loadRecycle(activeId, recyclePage)
  }, [tab, activeId, recyclePage, loadRecycle])

  const refreshFiles = useCallback(() => {
    if (!activeId) return
    void loadFiles(activeId, path, page, query)
  }, [activeId, path, page, query, loadFiles])

  const refreshRef = useRef<() => void>(() => {})
  useEffect(() => {
    refreshRef.current = refreshFiles
  }, [refreshFiles])

  /* ---------------- transfer events ---------------- */

  useEffect(() => {
    const offProgress = onEvent<{ key?: string; kind?: string; done?: number; total?: number }>(
      'transfer:progress',
      (ev) => {
        if (ev?.kind !== 'netdisk-upload' || !ev.key) return
        const key = ev.key
        setUploads((prev) => {
          const item: UploadProgress = { key, done: typeof ev.done === 'number' ? ev.done : 0, total: typeof ev.total === 'number' ? ev.total : 0 }
          const idx = prev.findIndex((u) => u.key === key)
          if (idx === -1) return [...prev, item]
          const next = [...prev]
          next[idx] = item
          return next
        })
      },
    )
    const offDone = onEvent<{ key?: string; kind?: string; path?: string }>('transfer:done', (ev) => {
      if (ev?.kind !== 'netdisk-upload') return
      setUploads((prev) => prev.filter((u) => u.key !== ev.key))
      toast.push(`上传完成：${ev.path || ''}`)
      refreshRef.current()
    })
    return () => {
      offProgress()
      offDone()
    }
  }, [toast])

  /* ---------------- account actions ---------------- */

  const selectAccount = (id: string) => {
    if (id === activeId) return
    setActiveId(id)
    setTab('files')
  }

  const refreshAccount = async (account: NetdiskAccount) => {
    setRefreshingId(account.id)
    try {
      const updated = await api.RefreshNetdiskAccount(account.id)
      setAccounts((prev) => prev.map((a) => (a.id === account.id ? updated : a)))
      toast.push(`已刷新：${updated.remark || updated.display_name || updated.id}`)
    } catch (err) {
      toast.error(err)
    } finally {
      setRefreshingId('')
    }
  }

  const removeAccount = async () => {
    if (!removeFor) return
    const target = removeFor
    setRemoveFor(null)
    try {
      await api.RemoveNetdiskAccount(target.id)
      toast.push('账号已删除')
      await loadAccounts()
    } catch (err) {
      toast.error(err)
    }
  }

  const addAccount = async () => {
    if (!addProvider) {
      toast.push('请选择网盘类型', 'warn')
      return
    }
    if (!addCookie.trim()) {
      toast.push('请粘贴浏览器 Cookie', 'warn')
      return
    }
    setAdding(true)
    try {
      const account = await api.AddNetdiskAccount(addProvider, addCookie.trim(), addName.trim())
      const label = account.remark || account.display_name || account.id
      toast.push(`已添加账号：${label}`)
      setAddOpen(false)
      setAddCookie('')
      setAddName('')
      await loadAccounts()
      setActiveId(account.id)
    } catch (err) {
      toast.error(err)
    } finally {
      setAdding(false)
    }
  }

  const updateCookie = async () => {
    if (!cookieFor) return
    if (!cookieText.trim()) {
      toast.push('请粘贴新的 Cookie', 'warn')
      return
    }
    setCookieBusy(true)
    try {
      const updated = await api.UpdateNetdiskCookie(cookieFor.id, cookieText.trim())
      setAccounts((prev) => prev.map((a) => (a.id === updated.id ? updated : a)))
      setCookieFor(null)
      setCookieText('')
      toast.push('Cookie 已更新')
    } catch (err) {
      toast.error(err)
    } finally {
      setCookieBusy(false)
    }
  }

  const updateRemark = async () => {
    if (!remarkFor) return
    setRemarkBusy(true)
    try {
      const updated = await api.UpdateNetdiskRemark(remarkFor.id, remarkText.trim())
      setAccounts((prev) => prev.map((a) => (a.id === updated.id ? updated : a)))
      setRemarkFor(null)
      setRemarkText('')
      toast.push('备注已更新')
    } catch (err) {
      toast.error(err)
    } finally {
      setRemarkBusy(false)
    }
  }

  /* ---------------- file actions ---------------- */

  const navigate = (target: string) => {
    if (!activeId) return
    setQuery('')
    setSearchInput('')
    setSelected([])
    setPath(target)
    setPage(1)
    void loadFiles(activeId, target, 1, '')
  }

  const toggleSelect = (file: NetdiskFile, additive: boolean) => {
    if (!file.id) return
    setSelected((prev) => {
      if (additive) {
        return prev.includes(file.id) ? prev.filter((id) => id !== file.id) : [...prev, file.id]
      }
      return prev.length === 1 && prev[0] === file.id ? [] : [file.id]
    })
  }

  const doSearch = () => {
    if (!activeId) return
    const q = searchInput.trim()
    setQuery(q)
    setSelected([])
    setPage(1)
    void loadFiles(activeId, path, 1, q)
  }

  const clearSearch = () => {
    if (!activeId) return
    setSearchInput('')
    setQuery('')
    setPage(1)
    setSelected([])
    void loadFiles(activeId, path, 1, '')
  }

  const changePage = (next: number) => {
    if (!activeId || next < 1) return
    setSelected([])
    setPage(next)
    void loadFiles(activeId, path, next, query)
  }

  const doMkdir = async () => {
    if (!activeId) return
    const name = mkdirName.trim()
    if (!name) {
      toast.push('请输入文件夹名称', 'warn')
      return
    }
    setMkdirBusy(true)
    try {
      await api.NetdiskMkdir({ account_id: activeId, path: path || undefined, name })
      setMkdirOpen(false)
      setMkdirName('')
      toast.push('文件夹已创建')
      refreshFiles()
    } catch (err) {
      toast.error(err)
    } finally {
      setMkdirBusy(false)
    }
  }

  const uploadFile = async () => {
    if (!activeId) return
    try {
      const local = await api.PickFile('选择要上传到网盘的文件')
      if (!local) return
      const res = await api.NetdiskUpload({ account_id: activeId, local_path: local, target_path: path || undefined })
      toast.push(`已上传：${res.name || local}`)
      refreshFiles()
    } catch (err) {
      toast.error(err)
    }
  }

  const downloadSelected = async () => {
    if (!activeId || !singleFile) return
    try {
      await api.NetdiskEnqueueDownload({
        account_id: activeId,
        file_id: singleFile.id,
        name: singleFile.name,
        path: singleFile.path,
        size: singleFile.size,
        md5: singleFile.md5,
      })
      toast.push(`已加入下载队列：${singleFile.name}`)
    } catch (err) {
      toast.error(err)
    }
  }

  const doRename = async () => {
    if (!activeId || !renameTarget) return
    const newName = renameName.trim()
    if (!newName) {
      toast.push('请输入新的名称', 'warn')
      return
    }
    if (newName === renameTarget.name) {
      setRenameTarget(null)
      return
    }
    setRenameBusy(true)
    try {
      await api.NetdiskRename({
        account_id: activeId,
        file_id: renameTarget.id,
        path: renameTarget.path,
        new_name: newName,
      })
      setRenameTarget(null)
      setRenameName('')
      toast.push('重命名成功')
      refreshFiles()
    } catch (err) {
      toast.error(err)
    } finally {
      setRenameBusy(false)
    }
  }

  const doDelete = async () => {
    if (!activeId || !deleteTargets) return
    const targets = deleteTargets
    setDeleteTargets(null)
    try {
      await api.NetdiskDelete({ account_id: activeId, file_ids: targets.map((f) => f.id) })
      setSelected([])
      toast.push('已删除到回收站')
      refreshFiles()
      if (tab === 'shares') void loadShares(activeId, sharePage)
    } catch (err) {
      toast.error(err)
    }
  }

  const doShare = async () => {
    if (!activeId || selectedFiles.length === 0) return
    setShareBusy(true)
    try {
      const title = selectedFiles.length === 1 ? selectedFiles[0].name : `${selectedFiles.length} 个文件`
      const res = await api.NetdiskCreateShare({
        account_id: activeId,
        file_ids: selectedFiles.map((f) => f.id),
        title,
        password: sharePassword.trim() || undefined,
        expire_days: shareExpire > 0 ? shareExpire : undefined,
      })
      setShareResult({ url: res.url, password: res.password || sharePassword.trim() })
      toast.push('分享已创建')
    } catch (err) {
      toast.error(err)
    } finally {
      setShareBusy(false)
    }
  }

  const closeShare = () => {
    setShareOpen(false)
    setShareResult(null)
    setSharePassword('')
    setShareExpire(7)
    if (activeId && tab === 'shares') {
      setSharePage(1)
      void loadShares(activeId, 1)
    }
  }

  const cancelShare = async (entry: ShareEntry) => {
    if (!activeId) return
    try {
      await api.NetdiskCancelShares(activeId, [entry.shareId || entry.key])
      toast.push('分享已取消')
      void loadShares(activeId, sharePage)
    } catch (err) {
      toast.error(err)
    }
  }

  const restoreRecycle = async (entry: RecycleEntry) => {
    if (!activeId) return
    try {
      await api.NetdiskRestoreRecycle(activeId, [entry.itemId])
      toast.push('已恢复到网盘')
      void loadRecycle(activeId, recyclePage)
    } catch (err) {
      toast.error(err)
    }
  }

  const purgeRecycle = async () => {
    if (!activeId || !recyclePurge) return
    const target = recyclePurge
    setRecyclePurge(null)
    try {
      await api.NetdiskDeleteRecycle(activeId, [target.itemId])
      toast.push('已永久删除')
      void loadRecycle(activeId, recyclePage)
    } catch (err) {
      toast.error(err)
    }
  }

  /* ---------------- render ---------------- */

  return (
    <div>
      <div className="card">
        <div className="row">
          <div style={{ flex: 1, minWidth: 0 }}>
            <h3 className="card-title">网盘账号</h3>
            <p className="card-desc" style={{ margin: 0 }}>
              夸克 / UC / 百度网盘统一管理，通过浏览器 Cookie 授权，可浏览、上传、下载、分享与转存。
            </p>
          </div>
          <button className="btn btn-sm" onClick={() => void loadAccounts()} disabled={accountsLoading}>
            刷新列表
          </button>
          <button className="btn btn-primary" onClick={() => setAddOpen(true)}>
            添加账号
          </button>
        </div>
        <div className="divider" />
        {accountsLoading ? (
          <Spinner label="读取网盘账号" />
        ) : accounts.length === 0 ? (
          <Empty icon="☁" title="还没有网盘账号" hint="点击右上角「添加账号」，粘贴浏览器 Cookie 即可接入夸克 / UC / 百度网盘" />
        ) : (
          <div className="grid-3">
            {accounts.map((account) => (
              <AccountCard
                key={account.id}
                account={account}
                providerName={providerName(account.provider)}
                active={account.id === activeId}
                refreshing={refreshingId === account.id}
                onSelect={() => selectAccount(account.id)}
                onRefresh={() => void refreshAccount(account)}
                onRename={() => {
                  setRemarkFor(account)
                  setRemarkText(account.remark || '')
                }}
                onCookie={() => {
                  setCookieFor(account)
                  setCookieText('')
                }}
                onRemove={() => setRemoveFor(account)}
              />
            ))}
          </div>
        )}
      </div>

      <div className="card">
        <Tabs<NdTab>
          tabs={[
            { id: 'files', label: '文件' },
            { id: 'shares', label: '分享' },
            { id: 'recycle', label: '回收站' },
          ]}
          value={tab}
          onChange={setTab}
        />

        {!activeAccount ? (
          <Empty icon="◇" title="请选择一个网盘账号" hint="在上方账号卡片中点击「打开」即可浏览文件" />
        ) : tab === 'files' ? (
          <div className="col">
            <div className="row row-wrap" style={{ gap: 8 }}>
              <div className="row" style={{ gap: 6, flex: 1, minWidth: 240 }}>
                <input
                  className="input"
                  placeholder="在当前账号内搜索文件名"
                  value={searchInput}
                  onChange={(e) => setSearchInput(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && doSearch()}
                />
                <button className="btn btn-sm" onClick={doSearch}>
                  搜索
                </button>
                {query ? (
                  <button className="btn btn-ghost btn-sm" onClick={clearSearch}>
                    清除
                  </button>
                ) : null}
              </div>
              <button className="btn btn-sm" onClick={refreshFiles} disabled={filesLoading}>
                刷新
              </button>
              <button className="btn btn-sm" onClick={() => setMkdirOpen(true)}>
                新建文件夹
              </button>
              <button className="btn btn-sm" onClick={() => void uploadFile()}>
                上传
              </button>
              <button className="btn btn-sm" onClick={() => void downloadSelected()} disabled={!singleFile}>
              <button
                className="btn btn-sm"
                title="递归下载当前目录全部文件"
                onClick={async () => {
                  try {
                    const res = await api.NetdiskEnqueueFolder(activeId, path)
                    toast.push(`已加入 ${res.queued ?? 0} 个文件到下载队列`)
                  } catch (err) {
                    toast.error(err)
                  }
                }}
              >
                下载全部
              </button>
                下载
              </button>
              <button className="btn btn-sm" onClick={() => setShareOpen(true)} disabled={selectedFiles.length === 0}>
                分享
              </button>
              <button
                className="btn btn-sm"
                onClick={() => {
                  const file = singleFile
                  if (!file) return
                  setRenameTarget(file)
                  setRenameName(file.name)
                }}
                disabled={!singleFile}
              >
                重命名
              </button>
              <button
                className="btn btn-danger btn-sm"
                onClick={() => setDeleteTargets(selectedFiles)}
                disabled={selectedFiles.length === 0}
              >
                删除
              </button>
            </div>

            {uploads.length > 0 ? (
              <div className="col" style={{ gap: 6 }}>
                {uploads.map((u) => (
                  <div className="row" key={u.key} style={{ gap: 10 }}>
                    <span className="small faint truncate mono" style={{ maxWidth: 320 }} title={u.key}>
                      正在上传 {u.key.replace('netdisk-upload:', '')}
                    </span>
                    <div style={{ flex: 1 }}>
                      <Progress percent={u.total > 0 ? percent(u.done, u.total) : 35} indeterminate={u.total <= 0} />
                    </div>
                    <span className="small mono faint nowrap">{formatBytes(u.done)}</span>
                  </div>
                ))}
              </div>
            ) : null}

            <div className="row" style={{ gap: 8 }}>
              <div className="crumbs" style={{ flex: 1 }}>
                <span className="crumb" onClick={() => navigate('')}>
                  {query ? `搜索结果（${fileList?.total ?? files.length}）` : '根目录'}
                </span>
                {query
                  ? null
                  : segments.map((seg) => (
                      <span key={seg.path} className="row" style={{ gap: 2 }}>
                        <span className="faint">/</span>
                        <span className="crumb" onClick={() => navigate(seg.path)}>
                          {seg.label}
                        </span>
                      </span>
                    ))}
              </div>
              <span className="small faint nowrap">
                {fileList?.capacity_total
                  ? `已用 ${formatBytes(fileList.capacity_used)} / ${formatBytes(fileList.capacity_total)}`
                  : selectedFiles.length > 0
                    ? `已选 ${selectedFiles.length} 项`
                    : ''}
              </span>
            </div>

            {filesLoading ? (
              <div style={{ padding: 20 }}>
                <Spinner label="读取目录" />
              </div>
            ) : files.length === 0 ? (
              <Empty
                icon="▤"
                title={query ? '没有匹配的文件' : '这个文件夹是空的'}
                hint={query ? '换个关键词试试' : '可以点击「上传」从本机传文件上来'}
              />
            ) : (
              <div className="list">
                {files.map((file, index) => (
                  <div
                    key={file.id || file.path || `row-${index}`}
                    className={`file-row ${selected.includes(file.id) ? 'selected' : ''}`}
                    onClick={(e) => toggleSelect(file, e.ctrlKey || e.metaKey)}
                    onDoubleClick={() => {
                      if (file.is_dir) navigate(file.path)
                    }}
                    title={file.is_dir ? '双击进入文件夹' : file.path}
                    onContextMenu={(e) => {
                      e.preventDefault()
                      setMenu({
                        x: e.clientX,
                        y: e.clientY,
                        items: [
                          ...(file.is_dir ? [] : [{ label: '下载', action: () => void api.NetdiskEnqueueDownload({ account_id: activeId, file_id: file.id, name: file.name, path: file.path, size: file.size }).then(() => toast.push('已加入下载队列')).catch((err) => toast.error(err)) }]),
                          { label: '删除', danger: true, action: () => void api.NetdiskDelete({ account_id: activeId, file_ids: [file.id] }).then(() => { toast.push('已删除'); refreshFiles() }).catch((err) => toast.error(err)) },
                        ],
                      })
                    }}
                  >
                    <span className="file-icon">{file.is_dir ? '📁' : '📄'}</span>
                    <span className="file-name">{file.name}</span>
                    <span className="file-size">{file.is_dir ? '文件夹' : formatBytes(file.size)}</span>
                    <span className="file-size" style={{ width: 132, textAlign: 'right' }}>
                      {file.updated_at ? formatTime(file.updated_at) : '—'}
                    </span>
                  </div>
                ))}
              </div>
            )}

            <div className="row">
              <span className="small faint">
                第 {page} 页 · 每页 {fileList?.page_size ?? FILE_PAGE_SIZE} 项
                {fileList?.total ? ` · 共 ${fileList.total} 项` : ''}
              </span>
              <div className="spacer" />
              <button className="btn btn-sm" onClick={() => changePage(page - 1)} disabled={page <= 1 || filesLoading}>
                上一页
              </button>
              <button className="btn btn-sm" onClick={() => changePage(page + 1)} disabled={!hasMore || filesLoading}>
                下一页
              </button>
            </div>
          </div>
        ) : tab === 'shares' ? (
          <div className="col">
            <div className="row">
              <span className="small faint">已创建的分享链接，可复制或取消分享。</span>
              <div className="spacer" />
              <button className="btn btn-sm" onClick={() => activeId && void loadShares(activeId, sharePage)} disabled={shareLoading}>
                刷新
              </button>
            </div>
            {shareLoading ? (
              <div style={{ padding: 20 }}>
                <Spinner label="读取分享列表" />
              </div>
            ) : shareRows.length === 0 ? (
              <>
                <Empty icon="↗" title="暂无分享" hint="在「文件」中选中文件后点击「分享」即可创建" />
                {!shareDetected && shareRaw ? (
                  <pre className="mono faint" style={{ maxHeight: 240, overflow: 'auto', margin: 0 }}>
                    {JSON.stringify(shareRaw, null, 2)}
                  </pre>
                ) : null}
              </>
            ) : (
              <div className="list">
                {shareRows.map((entry) => (
                  <div className="list-row" key={entry.key}>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div className="name truncate" title={entry.title}>
                        {entry.title}
                      </div>
                      <div className="meta truncate mono" title={entry.url}>
                        {entry.url || '（未返回链接）'}
                        {entry.password ? ` · 提取码 ${entry.password}` : ''}
                      </div>
                    </div>
                    {entry.fileCount > 0 ? <span className="small faint nowrap">{entry.fileCount} 个文件</span> : null}
                    {entry.viewCount > 0 ? <span className="small faint nowrap">{entry.viewCount} 次浏览</span> : null}
                    {entry.expiredAt ? <span className="small faint nowrap">到期 {formatTime(entry.expiredAt)}</span> : null}
                    {entry.createdAt ? <span className="small faint nowrap">{formatTime(entry.createdAt)}</span> : null}
                    <button
                      className="btn btn-sm"
                      disabled={!entry.url}
                      onClick={async () => {
                        const ok = await copyText(entry.password ? `${entry.url} 提取码：${entry.password}` : entry.url)
                        toast.push(ok ? '链接已复制' : '复制失败', ok ? 'info' : 'warn')
                      }}
                    >
                      复制
                    </button>
                    <button className="btn btn-danger btn-sm" onClick={() => void cancelShare(entry)} disabled={!entry.shareId}>
                      取消分享
                    </button>
                  </div>
                ))}
              </div>
            )}
            <div className="row">
              <span className="small faint">第 {sharePage} 页</span>
              <div className="spacer" />
              <button
                className="btn btn-sm"
                onClick={() => setSharePage((p) => Math.max(1, p - 1))}
                disabled={sharePage <= 1 || shareLoading}
              >
                上一页
              </button>
              <button
                className="btn btn-sm"
                onClick={() => setSharePage((p) => p + 1)}
                disabled={!shareHasMore || shareLoading}
              >
                下一页
              </button>
            </div>
          </div>
        ) : (
          <div className="col">
            <div className="row">
              <span className="small faint">回收站中的文件可恢复或永久删除。</span>
              <div className="spacer" />
              <button
                className="btn btn-sm"
                onClick={() => activeId && void loadRecycle(activeId, recyclePage)}
                disabled={recycleLoading}
              >
                刷新
              </button>
            </div>
            {recycleLoading ? (
              <div style={{ padding: 20 }}>
                <Spinner label="读取回收站" />
              </div>
            ) : recycleRows.length === 0 ? (
              <>
                <Empty icon="🗑" title="回收站是空的" hint="删除的文件会先进入回收站" />
                {!recycleDetected && recycleRaw ? (
                  <pre className="mono faint" style={{ maxHeight: 240, overflow: 'auto', margin: 0 }}>
                    {JSON.stringify(recycleRaw, null, 2)}
                  </pre>
                ) : null}
              </>
            ) : (
              <div className="list">
                {recycleRows.map((entry) => (
                  <div className="list-row" key={entry.key}>
                    <span className="file-icon">{entry.isDir ? '📁' : '📄'}</span>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div className="name truncate" title={entry.name}>
                        {entry.name}
                      </div>
                      <div className="meta truncate mono" title={entry.path}>
                        {entry.path || '—'}
                      </div>
                    </div>
                    <span className="file-size nowrap">{entry.isDir ? '文件夹' : formatBytes(entry.size)}</span>
                    {entry.deletedAt ? <span className="small faint nowrap">{formatTime(entry.deletedAt)}</span> : null}
                    <button className="btn btn-sm" onClick={() => void restoreRecycle(entry)} disabled={!entry.itemId}>
                      恢复
                    </button>
                    <button className="btn btn-danger btn-sm" onClick={() => setRecyclePurge(entry)} disabled={!entry.itemId}>
                      永久删除
                    </button>
                  </div>
                ))}
              </div>
            )}
            <div className="row">
              <span className="small faint">第 {recyclePage} 页</span>
              <div className="spacer" />
              <button
                className="btn btn-sm"
                onClick={() => setRecyclePage((p) => Math.max(1, p - 1))}
                disabled={recyclePage <= 1 || recycleLoading}
              >
                上一页
              </button>
              <button
                className="btn btn-sm"
                onClick={() => setRecyclePage((p) => p + 1)}
                disabled={!recycleHasMore || recycleLoading}
              >
                下一页
              </button>
            </div>
          </div>
        )}
      </div>

      {/* ---------------- dialogs ---------------- */}

      {addOpen ? (
        <Modal
          title="添加网盘账号"
          wide
          onClose={() => {
            if (adding) return
            setAddOpen(false)
          }}
          footer={
            <>
              <button className="btn" onClick={() => setAddOpen(false)} disabled={adding}>
                取消
              </button>
              <button className="btn btn-primary" onClick={() => void addAccount()} disabled={adding}>
                {adding ? '正在校验 Cookie…' : '添加'}
              </button>
            </>
          }
        >
          <Field label="网盘类型">
            <select className="select" value={addProvider} onChange={(e) => setAddProvider(e.target.value)} disabled={adding}>
              {providers.length === 0 ? <option value="">（未读取到可用网盘）</option> : null}
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name || PROVIDER_FALLBACK[p.id] || p.id}
                </option>
              ))}
            </select>
          </Field>
          <Field label="浏览器 Cookie" hint="必须是完整的请求头 Cookie 值，包含所有 key=value，以分号分隔">
            <textarea
              className="textarea"
              style={{ minHeight: 140 }}
              placeholder="粘贴形如 __pus=...; __kp=...; __uid=...; 的完整 Cookie"
              value={addCookie}
              onChange={(e) => setAddCookie(e.target.value)}
              disabled={adding}
            />
          </Field>
          <Field label="备注名（可选）" hint="用于区分多个账号，留空则使用网盘返回的昵称">
            <input className="input" value={addName} onChange={(e) => setAddName(e.target.value)} disabled={adding} />
          </Field>
          <div className="small faint" style={{ lineHeight: 1.7 }}>
            获取方式：浏览器登录网盘网页版 → 按 F12 打开开发者工具 → 切换到 Network（网络）→ 刷新页面 → 任选一个请求 → 在
            Request Headers 中找到 <span className="mono">Cookie</span>，整段复制粘贴到上方。Cookie 等同于账号登录凭证，请勿
            泄露给他人。首次校验可能需要约 30 秒。
          </div>
          {adding ? <Spinner label="正在向网盘校验 Cookie，请稍候…" /> : null}
        </Modal>
      ) : null}

      {cookieFor ? (
        <Modal
          title={`更新 Cookie · ${cookieFor.remark || cookieFor.display_name || cookieFor.id}`}
          onClose={() => !cookieBusy && setCookieFor(null)}
          footer={
            <>
              <button className="btn" onClick={() => setCookieFor(null)} disabled={cookieBusy}>
                取消
              </button>
              <button className="btn btn-primary" onClick={() => void updateCookie()} disabled={cookieBusy}>
                保存
              </button>
            </>
          }
        >
          <Field label="新的 Cookie" hint="Cookie 过期后账号会显示异常，重新粘贴即可恢复正常">
            <textarea
              className="textarea"
              style={{ minHeight: 120 }}
              value={cookieText}
              onChange={(e) => setCookieText(e.target.value)}
              placeholder="粘贴新的完整 Cookie"
            />
          </Field>
          {cookieFor.cookie_hint ? <div className="small faint mono truncate">{cookieFor.cookie_hint}</div> : null}
          {cookieBusy ? <Spinner label="正在校验…" /> : null}
        </Modal>
      ) : null}

      {remarkFor ? (
        <Modal
          title="重命名账号备注"
          onClose={() => !remarkBusy && setRemarkFor(null)}
          footer={
            <>
              <button className="btn" onClick={() => setRemarkFor(null)} disabled={remarkBusy}>
                取消
              </button>
              <button className="btn btn-primary" onClick={() => void updateRemark()} disabled={remarkBusy}>
                保存
              </button>
            </>
          }
        >
          <Field label="备注名" hint={`当前昵称：${remarkFor.display_name || '—'}`}>
            <input className="input" value={remarkText} onChange={(e) => setRemarkText(e.target.value)} autoFocus />
          </Field>
        </Modal>
      ) : null}

      {removeFor ? (
        <Confirm
          title="删除网盘账号"
          danger
          message={`确定要删除账号「${removeFor.remark || removeFor.display_name || removeFor.id}」吗？本地保存的 Cookie 也会一并清除，网盘上的文件不受影响。`}
          onCancel={() => setRemoveFor(null)}
          onConfirm={() => void removeAccount()}
        />
      ) : null}

      {mkdirOpen ? (
        <Modal
          title="新建文件夹"
          onClose={() => !mkdirBusy && setMkdirOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setMkdirOpen(false)} disabled={mkdirBusy}>
                取消
              </button>
              <button className="btn btn-primary" onClick={() => void doMkdir()} disabled={mkdirBusy}>
                创建
              </button>
            </>
          }
        >
          <Field label="文件夹名称" hint={`创建位置：${path || '根目录'}`}>
            <input
              className="input"
              value={mkdirName}
              onChange={(e) => setMkdirName(e.target.value)}
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && void doMkdir()}
            />
          </Field>
        </Modal>
      ) : null}

      {renameTarget ? (
        <Modal
          title="重命名"
          onClose={() => !renameBusy && setRenameTarget(null)}
          footer={
            <>
              <button className="btn" onClick={() => setRenameTarget(null)} disabled={renameBusy}>
                取消
              </button>
              <button className="btn btn-primary" onClick={() => void doRename()} disabled={renameBusy}>
                保存
              </button>
            </>
          }
        >
          <Field label="新名称">
            <input
              className="input"
              value={renameName}
              onChange={(e) => setRenameName(e.target.value)}
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && void doRename()}
            />
          </Field>
        </Modal>
      ) : null}

      {shareOpen ? (
        <Modal
          title="创建分享链接"
          onClose={closeShare}
          footer={
            shareResult ? (
              <button className="btn btn-primary" onClick={closeShare}>
                完成
              </button>
            ) : (
              <>
                <button className="btn" onClick={closeShare} disabled={shareBusy}>
                  取消
                </button>
                <button className="btn btn-primary" onClick={() => void doShare()} disabled={shareBusy}>
                  创建分享
                </button>
              </>
            )
          }
        >
          {shareResult ? (
            <>
              <Field label="分享链接">
                <div className="row" style={{ gap: 8 }}>
                  <input className="input mono" readOnly value={shareResult.url} onFocus={(e) => e.target.select()} />
                  <button
                    className="btn btn-sm"
                    onClick={async () => {
                      const ok = await copyText(shareResult.password ? `${shareResult.url} 提取码：${shareResult.password}` : shareResult.url)
                      toast.push(ok ? '链接已复制' : '复制失败', ok ? 'info' : 'warn')
                    }}
                  >
                    复制
                  </button>
                </div>
              </Field>
              {shareResult.password ? (
                <Field label="提取码">
                  <input className="input mono" readOnly value={shareResult.password} />
                </Field>
              ) : null}
            </>
          ) : (
            <>
              <div className="small dim">
                已选 {selectedFiles.length} 项：{selectedFiles.map((f) => f.name).join('、') || '—'}
              </div>
              <Field label="提取码（可选）" hint="留空则由网盘随机生成">
                <input
                  className="input mono"
                  placeholder="例如 8a6c"
                  maxLength={8}
                  value={sharePassword}
                  onChange={(e) => setSharePassword(e.target.value)}
                />
              </Field>
              <Field label="有效期（天）" hint="0 表示使用网盘默认或永久有效">
                <input
                  className="input"
                  type="number"
                  min={0}
                  max={365}
                  value={shareExpire}
                  onChange={(e) => setShareExpire(Math.max(0, Math.min(365, Number(e.target.value) || 0)))}
                />
              </Field>
              {shareBusy ? <Spinner label="正在创建分享…" /> : null}
            </>
          )}
        </Modal>
      ) : null}

      {deleteTargets ? (
        <Confirm
          title="删除文件"
          danger
          message={`确定要删除选中的 ${deleteTargets.length} 项吗？文件会移入网盘回收站。`}
          onCancel={() => setDeleteTargets(null)}
          onConfirm={() => void doDelete()}
        />
      ) : null}

      {recyclePurge ? (
        <Confirm
          title="永久删除"
          danger
          message={`确定要从回收站永久删除「${recyclePurge.name}」吗？此操作不可恢复。`}
          onCancel={() => setRecyclePurge(null)}
          onConfirm={() => void purgeRecycle()}
        />
      ) : null}
      {menu && (
        <div className="ctx-backdrop" onClick={() => setMenu(null)} onContextMenu={(e) => { e.preventDefault(); setMenu(null) }}>
          <div className="ctx-menu" style={{ left: menu.x, top: menu.y }} onClick={(e) => e.stopPropagation()}>
            {menu.items.map((item, i) => (
              <button key={i} className={`ctx-item ${item.danger ? 'danger' : ''}`} onClick={() => { setMenu(null); item.action() }}>
                {item.label}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
/* ---------------- account card ---------------- */

function AccountCard({
  account,
  providerName,
  active,
  refreshing,
  onSelect,
  onRefresh,
  onRename,
  onCookie,
  onRemove,
}: {
  account: NetdiskAccount
  providerName: string
  active: boolean
  refreshing: boolean
  onSelect: () => void
  onRefresh: () => void
  onRename: () => void
  onCookie: () => void
  onRemove: () => void
}) {
  const used = account.used_bytes || 0
  const total = account.total_bytes || 0
  const pct = percent(used, total)
  const ok = (account.status || '').toLowerCase() === 'active'
  const label = account.remark || account.display_name || account.id

  return (
    <div className="scope-card" style={active ? { borderColor: 'rgba(61, 220, 151, 0.45)' } : undefined}>
      <div className="scope-head">
        <Tag kind="info">{providerName}</Tag>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="scope-title truncate" title={label}>
            {label}
          </div>
          <div className="scope-desc truncate">{account.member_type || (account.display_name && account.display_name !== label ? account.display_name : '')}</div>
        </div>
        <span title={account.error || (ok ? '账号状态正常' : 'Cookie 可能已失效')}>
          {ok ? <Tag kind="ok">正常</Tag> : <Tag kind="bad">异常</Tag>}
        </span>
      </div>

      <div className="col" style={{ gap: 4 }}>
        <div className="row small faint" style={{ justifyContent: 'space-between' }}>
          <span className="mono">
            {formatBytes(used)} / {formatBytes(total)}
          </span>
          <span>{total > 0 ? `${pct.toFixed(1)}%` : '容量未知'}</span>
        </div>
        <Progress percent={pct} />
      </div>

      {account.cookie_hint ? (
        <div className="small faint mono truncate" title={account.cookie_hint}>
          {account.cookie_hint}
        </div>
      ) : null}
      {account.error ? (
        <div className="small truncate" style={{ color: 'var(--danger)' }} title={account.error}>
          {account.error}
        </div>
      ) : null}

      <div className="row row-wrap" style={{ gap: 6 }}>
        <button className={`btn btn-sm ${active ? 'btn-primary' : ''}`} onClick={onSelect}>
          {active ? '浏览中' : '打开'}
        </button>
        <button className="btn btn-sm" onClick={onRefresh} disabled={refreshing}>
          {refreshing ? '刷新中…' : '刷新'}
        </button>
        <button className="btn btn-sm" onClick={onRename}>
          重命名
        </button>
        <button className="btn btn-sm" onClick={onCookie}>
          更新 Cookie
        </button>
        <div className="spacer" />
        <button className="btn btn-danger btn-sm" onClick={onRemove}>
          删除
        </button>
      </div>
    </div>
  )
}


