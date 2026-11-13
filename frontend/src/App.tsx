import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, onEvent, type ServerStatus, type Settings } from './lib/api'
import { ToastProvider, useToast } from './components/ui'
import { ErrorBoundary } from './components/ErrorBoundary'
import NotesPage from './pages/NotesPage'
import FilesPage from './pages/FilesPage'
import SSHPage from './pages/SSHPage'
import FtpPage from './pages/FtpPage'
import DownloadsPage from './pages/DownloadsPage'
import NetdiskPage from './pages/NetdiskPage'
import SubscriptionPage from './pages/SubscriptionPage'
import ScreenshotsPage from './pages/ScreenshotsPage'
import UpdatePage from './pages/UpdatePage'
import ProxyPage from './pages/ProxyPage'
import ServerPage from './pages/ServerPage'
import SettingsPage from './pages/SettingsPage'

type PageKey =
  | 'notes'
  | 'files'
  | 'ssh'
  | 'ftp'
  | 'downloads'
  | 'netdisk'
  | 'screenshots'
  | 'proxy'
  | 'subscription'
  | 'server'
  | 'update'
  | 'settings'

interface NavDef {
  key: PageKey
  label: string
  icon: string
  title: string
  sub: string
  section: string
}

const NAV: NavDef[] = [
  { key: 'notes', label: '云笔记', icon: '✎', title: '云同步笔记', sub: 'Markdown 笔记，跨设备同步', section: '工作区' },
  { key: 'files', label: '文件', icon: '▤', title: '文件管理', sub: '本地 / SFTP / 云端文件互传', section: '工作区' },
  { key: 'downloads', label: '下载', icon: '↓', title: '下载工具', sub: 'HTTP 多线程下载，支持代理与断点续传', section: '工作区' },
  { key: 'netdisk', label: '网盘', icon: '☁', title: '网盘管理', sub: '夸克 / UC / 百度网盘统一管理', section: '工作区' },
  { key: 'screenshots', label: '截图', icon: '▣', title: '截图自动保存', sub: '一键截屏、剪贴板图片自动落盘', section: '工作区' },
  { key: 'ssh', label: 'SSH', icon: '⌘', title: 'SSH 管理', sub: '主机、密钥、跳板机与中转隧道', section: '连接' },
  { key: 'ftp', label: 'FTP', icon: '⇄', title: 'FTP 管理', sub: 'FTP / FTPS 浏览与传输', section: '连接' },
  { key: 'server', label: '同步服务', icon: '◎', title: '同步服务器', sub: 'UDP(QUIC) 优先，TCP 回退', section: '连接' },
  { key: 'proxy', label: '代理', icon: '⛓', title: '代理设置', sub: '按连接类型分别设置走代理或不走', section: '设置' },
  { key: 'subscription', label: '订阅', icon: '⇅', title: '代理订阅', sub: '订阅链接自动更新节点，一键应用为代理', section: '设置' },
  { key: 'update', label: '更新', icon: '⟳', title: '自动更新', sub: '服务器分发构建，GitHub 回退（SNI 绕过）', section: '设置' },
  { key: 'settings', label: '设置', icon: '⚙', title: '设置', sub: '下载、外观与数据目录', section: '设置' },
]

function Shell() {
  const toast = useToast()
  const [page, setPage] = useState<PageKey>('notes')
  const [settings, setSettings] = useState<Settings | null>(null)
  const [status, setStatus] = useState<ServerStatus | null>(null)
  const [theme, setTheme] = useState('dark')
  const [version, setVersion] = useState('')

  const loadSettings = useCallback(async () => {
    try {
      const s = await api.GetSettings()
      setSettings(s)
      setTheme(s.ui?.theme || 'dark')
    } catch (err) {
      toast.error(err)
    }
  }, [toast])

  const refreshStatus = useCallback(async () => {
    try {
      setStatus(await api.ServerStatus())
    } catch {
      /* backend may still be warming up */
    }
  }, [])

  useEffect(() => {
    void loadSettings()
    void refreshStatus()
    api
      .UpdateCurrentVersion()
      .then(setVersion)
      .catch(() => {})
  }, [loadSettings, refreshStatus])

  useEffect(() => {
    const off = onEvent<ServerStatus>('server:status', (st) => setStatus(st))
    const offErr = onEvent<string>('server:error', (msg) => toast.push(msg, 'warn'))
    return () => {
      off()
      offErr()
    }
  }, [toast])

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

  const current = useMemo(() => NAV.find((n) => n.key === page) ?? NAV[0], [page])

  const sections = useMemo(() => {
    const map = new Map<string, NavDef[]>()
    for (const item of NAV) {
      const list = map.get(item.section) ?? []
      list.push(item)
      map.set(item.section, list)
    }
    return [...map.entries()]
  }, [])

  const toggleTheme = async () => {
    const next = theme === 'dark' ? 'light' : 'dark'
    setTheme(next)
    if (!settings) return
    try {
      const saved = await api.SaveSettings({ ...settings, ui: { ...settings.ui, theme: next } })
      setSettings(saved)
    } catch (err) {
      toast.error(err)
    }
  }

  const transportChip = () => {
    if (!status?.connected) return <span className="tag">未连接服务器</span>
    const t = (status.transport || '').toLowerCase()
    return (
      <span className={`tag ${t === 'udp' ? 'tag-ok' : 'tag-info'}`}>
        {t === 'udp' ? 'UDP · QUIC' : 'TCP 回退'} · {status.address}
      </span>
    )
  }

  const pageProps = { settings, reloadSettings: loadSettings, serverStatus: status, refreshServerStatus: refreshStatus }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">WT</div>
          <div>
            <div className="brand-name">WailsTools</div>
            <div className="brand-sub">云笔记 · 文件 · SSH</div>
          </div>
        </div>

        {sections.map(([section, items]) => (
          <div key={section}>
            <div className="nav-section">{section}</div>
            {items.map((item) => (
              <button
                key={item.key}
                className={`nav-item ${page === item.key ? 'active' : ''}`}
                onClick={() => setPage(item.key)}
              >
                <span className="nav-icon">{item.icon}</span>
                <span>{item.label}</span>
              </button>
            ))}
          </div>
        ))}

        <div className="sidebar-footer">
          <div className="row" style={{ justifyContent: 'space-between' }}>
            <span>{status?.user?.username ? `@${status.user.username}` : '未登录'}</span>
            <button className="btn btn-ghost btn-sm" onClick={toggleTheme} title="切换主题">
              {theme === 'dark' ? '☾' : '☀'}
            </button>
          </div>
          <div>{version ? `v${version}` : ''}</div>
        </div>
      </aside>

      <div className="main-area">
        <header className="topbar">
          <div>
            <h1>{current.title}</h1>
            <div className="sub">{current.sub}</div>
          </div>
          <div className="topbar-spacer" />
          {transportChip()}
          <button
            className="btn btn-sm"
            onClick={async () => {
              try {
                await api.ConnectServer()
                toast.push('服务器连接成功')
                void refreshStatus()
              } catch (err) {
                toast.error(err)
              }
            }}
          >
            连接
          </button>
        </header>

        <main className="page">
          <ErrorBoundary key={page} label={current.title}>
            {page === 'notes' && <NotesPage {...pageProps} />}
            {page === 'files' && <FilesPage {...pageProps} />}
            {page === 'downloads' && <DownloadsPage {...pageProps} />}
            {page === 'netdisk' && <NetdiskPage {...pageProps} />}
            {page === 'ssh' && <SSHPage {...pageProps} />}
            {page === 'ftp' && <FtpPage {...pageProps} />}
            {page === 'server' && <ServerPage {...pageProps} />}
            {page === 'screenshots' && <ScreenshotsPage {...pageProps} />}
            {page === 'proxy' && <ProxyPage {...pageProps} />}
            {page === 'subscription' && <SubscriptionPage {...pageProps} />}
            {page === 'update' && <UpdatePage {...pageProps} />}
            {page === 'settings' && <SettingsPage {...pageProps} />}
          </ErrorBoundary>
        </main>
      </div>
    </div>
  )
}

export interface PageProps {
  settings: Settings | null
  reloadSettings: () => Promise<void>
  serverStatus: ServerStatus | null
  refreshServerStatus: () => Promise<void>
}

export default function App() {
  return (
    <ToastProvider>
      <Shell />
    </ToastProvider>
  )
}




