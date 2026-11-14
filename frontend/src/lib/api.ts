/**
 * Typed bridge to the Go backend. Wails binds the App struct to
 * window.go.main.App; this module wraps those calls so pages stay typed and
 * free of `any`.
 */

declare global {
  interface Window {
    go?: Record<string, { App?: Record<string, (...args: any[]) => Promise<any>> }>
    runtime?: {
      EventsOn?: (name: string, cb: (...data: any[]) => void) => () => void
      EventsOff?: (name: string) => void
      ClipboardSetText?: (text: string) => Promise<boolean>
    }
  }
}

type Bridge = Record<string, (...args: any[]) => Promise<any>>

let cachedBridge: Bridge | null = null

/**
 * Wails v2 namespaces bindings by Go package name, so the App struct exported
 * from package `app` lands on window.go.app.App. Resolve defensively so a
 * package rename (or a dev build) never breaks the UI.
 */
function resolveBridge(): Bridge | null {
  if (cachedBridge) return cachedBridge
  const go = window.go
  if (!go) return null
  const preferred = ['app', 'main']
  for (const ns of preferred) {
    const cand = go[ns]?.App
    if (cand) {
      cachedBridge = cand
      return cand
    }
  }
  for (const ns of Object.keys(go)) {
    const cand = go[ns]?.App
    if (cand) {
      cachedBridge = cand
      return cand
    }
  }
  return null
}

function bridge(): Bridge {
  const b = resolveBridge()
  if (!b) {
    throw new Error('后端尚未就绪，请稍候重试')
  }
  return b
}

/** Waits briefly for the Wails bridge to be injected (first paint race). */
export async function waitForBackend(timeoutMs = 4000): Promise<boolean> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (resolveBridge()) return true
    await new Promise((r) => window.setTimeout(r, 60))
  }
  return resolveBridge() !== null
}

async function call<T>(method: string, ...args: any[]): Promise<T> {
  let b = resolveBridge()
  if (!b) {
    await waitForBackend(3000)
    b = resolveBridge()
  }
  if (!b) {
    throw new Error(`后端尚未就绪，无法调用 ${method}`)
  }
  const fn = b[method]
  if (typeof fn !== 'function') {
    throw new Error(`后端缺少方法: ${method}`)
  }
  try {
    return (await fn(...args)) as T
  } catch (err: any) {
    throw new Error(typeof err === 'string' ? err : err?.message ?? String(err))
  }
}

/* ---------------- shared types ---------------- */

export type ProxyMode = 'off' | 'http' | 'socks5' | 'inherit'

export interface ProxyRule {
  mode: ProxyMode | string
  url: string
  username?: string
  password?: string
  noProxy?: string[]
}

export interface ProxyInfo {
  rule: ProxyRule
  override: boolean
  url: string
}

export interface TestResult {
  ok: boolean
  detail: string
  latencyMs: number
  statusCode: number
  viaProxy: boolean
  proxyAddr?: string
}

export interface DownloadSettings {
  directory: string
  maxConcurrency: number
  connectionsPerTask: number
  chunkSizeBytes: number
  userAgent: string
  speedLimitKBps: number
  retryCount: number
  autoResume: boolean
}

export interface ServerSettings {
  host: string
  port: number
  token?: string
  username?: string
  pin?: string
  preferUdp: boolean
  disableUdp: boolean
  autoConnect: boolean
  lastUsedTransport?: string
}

export interface StartupSettings {
  enabled: boolean
  startMinimized: boolean
  trayEnabled: boolean
  closeToTray: boolean
}

export interface Settings {
  proxy: { mode?: string; default: ProxyRule; scopes?: Record<string, ProxyRule>; followSystem: boolean }
  downloads: DownloadSettings
  server: ServerSettings
  ui: { theme: string; language: string; density: string }
  update?: UpdateSettings
  screenshots?: ScreenshotSettings
  startup?: StartupSettings
}

export interface AppInfo {
  version: string
  dataDir: string
  platform: string
  goVersion: string
  notesPath: string
}

export interface FileEntry {
  name: string
  path: string
  isDir: boolean
  size: number
  modTime: string
}

export interface LocalListing {
  path: string
  parent: string
  entries: FileEntry[]
  home: string
  separator: string
}

/* ---------------- SSH ---------------- */

export interface SSHHost {
  id?: string
  name: string
  address: string
  port: number
  user: string
  authMode: string
  keyId?: string
  password?: string
  route: string
  jumpIds?: string[]
  relayTarget?: string
  group?: string
  color?: string
  notes?: string
  tags?: string[]
  hostKeyFingerprint?: string
  lastUsed?: string
  lastTransport?: string
  createdAt?: string
  updatedAt?: string
}

export interface SSHKey {
  id: string
  name: string
  type: string
  bits?: number
  comment?: string
  publicKey: string
  fingerprint: string
  createdAt: string
}

export interface DialOptions {
  passphrase?: string
  password?: string
  insecureHostKey?: boolean
  timeout?: number
}

export interface SSHSession {
  id: string
  hostId: string
  name: string
  address: string
  user: string
  transport: string
  chain?: string[]
  openedAt: string
}

export interface RemoteFile {
  name: string
  path: string
  isDir: boolean
  isLink: boolean
  size: number
  mode: string
  modTime: string
}

/* ---------------- FTP ---------------- */

export interface FTPProfile {
  id?: string
  name: string
  config: {
    id?: string
    name?: string
    host: string
    port: number
    user: string
    password?: string
    tls: boolean
    explicitFtps: boolean
    passive: boolean
    timeoutSec: number
    path?: string
  }
  password?: string
  createdAt?: string
  updatedAt?: string
}

export interface FTPSession {
  id: string
  name: string
  config: FTPProfile['config']
  openedAt: string
  path: string
}

export interface FTPEntry {
  name: string
  path: string
  isDir: boolean
  size: number
  mode: string
  modTime: string
}

/* ---------------- downloads ---------------- */

export type DownloadStatus =
  | 'queued'
  | 'preparing'
  | 'downloading'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'canceled'

export interface DownloadTask {
  id: string
  url: string
  finalUrl?: string
  filename: string
  directory: string
  total: number
  downloaded: number
  status: DownloadStatus
  error?: string
  connections: number
  speedBps: number
  percent: number
  etag?: string
  rangeOk: boolean
  proxyMode: string
  proxyUsed?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
  retries: number
}

export interface DownloadRequest {
  url: string
  filename?: string
  directory?: string
  headers?: Record<string, string>
  referer?: string
  connections?: number
  proxyMode?: string
}

/* ---------------- notes ---------------- */

export interface Note {
  id: string
  title: string
  content: string
  folder?: string
  tags?: string[]
  pinned: boolean
  deleted: boolean
  updatedAt: number
  createdAt: number
  revision: number
  dirty?: boolean
}

export interface SyncResult {
  pushed: number
  pulled: number
  conflicts: number
  serverTime: number
  transport: string
  message: string
}

/* ---------------- server ---------------- */

export interface ServerUser {
  id: string
  username: string
  displayName?: string
  email?: string
  role: string
  quotaBytes: number
  usedBytes: number
  createdAt: string
}

export interface ServerStatus {
  connected: boolean
  transport: string
  address: string
  user?: ServerUser
  fingerprint?: string
  udpError?: string
  tcpError?: string
  udpMs: number
  tcpMs: number
  lastError?: string
  since?: string
}

export interface RemoteEntry {
  name: string
  path: string
  isDir: boolean
  size: number
  modTime: string
}

/* ---------------- netdisk ---------------- */

export interface NetdiskProvider {
  id: string
  name: string
  capabilities: string[]
}

export interface NetdiskAccount {
  id: string
  provider: string
  display_name: string
  remark?: string
  avatar_url?: string
  member_type?: string
  used_bytes: number
  total_bytes: number
  status: string
  error?: string
  cookie_hint?: string
  capabilities: string[]
  created_at: string
  updated_at: string
}

export interface NetdiskFile {
  id: string
  provider: string
  account_id: string
  name: string
  path: string
  parent_id?: string
  is_dir: boolean
  size: number
  md5?: string
  updated_at?: string
  thumbnail?: string
}

export interface NetdiskFileList {
  account_id: string
  path: string
  parent_id?: string
  files: NetdiskFile[]
  page?: number
  page_size?: number
  total?: number
  has_more?: boolean
  capacity_used?: number
  capacity_total?: number
}


/* ---------------- subscriptions ---------------- */

export interface SubscriptionNode {
  name: string
  scheme: string
  host: string
  port: number
  username?: string
  password?: string
}

export interface SubscriptionInfo {
  upload: number
  download: number
  total: number
  expire: number
}

export interface SubscriptionSource {
  id: string
  name: string
  url: string
  enabled: boolean
  intervalHours: number
  lastFetch?: string
  lastError?: string
  nodes: MiproxyNode[]
  info: SubscriptionInfo
  createdAt?: string
  updatedAt?: string
}

/* ---------------- screenshots ---------------- */

export interface ScreenshotItem {
  path: string
  name: string
  size: number
  width?: number
  height?: number
  createdAt: string
  source: string
}

export interface ScreenshotSettings {
  directory: string
  pattern: string
  format: string
  autoSave: boolean
  watchClipboard: boolean
  copyToClipboard: boolean
  openAfterCapture: boolean
  hotkey?: string
  autoUpload?: boolean
}

/* ---------------- updates ---------------- */

export interface UpdateRelease {
  id: string
  platform: string
  arch: string
  channel: string
  version: string
  filename: string
  size: number
  sha256?: string
  notes?: string
  source: string
  url?: string
  downloads?: number
  mandatory?: boolean
  createdAt?: string
}

export interface UpdateStatus {
  current: string
  latest: string
  available: boolean
  source: string
  message: string
  checkedAt: string
  error?: string
  transport?: string
  currentHash?: string
  releaseHash?: string
  sameBuild?: boolean
  release?: UpdateRelease
}

export interface UpdateSettings {
  source: string
  channel: string
  autoCheck: boolean
  autoInstall: boolean
  githubRepo: string
  githubBypass: boolean
  githubFragment: boolean
  lastCheckAt?: string
}

export interface AutostartStatus {
  enabled: boolean
  command?: string
  location?: string
  supported: boolean
  error?: string
}

/* ---------------- proxy engine nodes ---------------- */

export interface MiproxyNode {
  id?: string
  name: string
  protocol: string
  server: string
  port: number
  uuid?: string
  alterId?: number
  security?: string
  flow?: string
  network?: string
  path?: string
  host?: string
  tls?: boolean
  sni?: string
  skipVerify?: boolean
  password?: string
  method?: string
  username?: string
  sourceId?: string
  index?: number
  raw?: string
}

export interface ProxyRuntimeStats {
  accepted: number
  active: number
  failed: number
  bytesIn: number
  bytesOut: number
  startedAt: string
}

export interface ProxyRuntimeStatus {
  running: boolean
  node?: MiproxyNode
  addr?: string
  external: boolean
  auth: boolean
  stats?: ProxyRuntimeStats
  since?: string
  error?: string
}

export interface ProxyRuntimeSettings {
  external: boolean
  port: number
  username?: string
  password?: string
  systemProxy?: boolean
}

export interface NodeTestResult {
  index: number
  latencyMs: number
  ok: boolean
  error?: string
}

export interface TransferTask {
  id: string
  kind: string
  label: string
  path: string
  total: number
  done: number
  status: string
  error?: string
  startedAt: string
  updatedAt: string
}

export interface SubSyncResult {
  local: number
  remote: number
  merged: number
  pushed: boolean
  message: string
}

export interface ActiveProxyInfo {
  mode: string
  running: boolean
  node?: MiproxyNode
  addr?: string
  external?: boolean
}

/* ---------------- proxy chain / rules / geoip ---------------- */

export interface ProxyChainSettings {
  mode: string
  nodeIds?: string[]
}

export interface DirectRuleSettings {
  useGeoIP: boolean
  countries?: string[]
  rules?: string[]
}

export interface GeoIPStatus {
  ready: boolean
  path: string
  size: number
  updatedAt?: string
  downloading: boolean
  error?: string
}

export interface LatencyEntry {
  key: string
  latencyMs: number
  ok: boolean
  error?: string
  at: string
}
/* ---------------- events ---------------- */

export type Unsubscribe = () => void

export function onEvent<T = any>(name: string, cb: (payload: T) => void): Unsubscribe {
  const rt = window.runtime
  if (!rt?.EventsOn) return () => {}
  const off = rt.EventsOn(name, (data: any) => cb(data as T))
  return typeof off === 'function' ? off : () => {}
}

/* ---------------- API surface ---------------- */

const rawApi = {
  // app / settings
  AppInfo: () => call<AppInfo>('AppInfo'),
  GetSettings: () => call<Settings>('GetSettings'),
  SaveSettings: (s: Settings) => call<Settings>('SaveSettings', s),
  ProxyScopes: () => call<string[]>('ProxyScopes'),
  GetProxy: (scope: string) => call<ProxyInfo>('GetProxy', scope),
  SetProxy: (scope: string, rule: ProxyRule) => call<Settings>('SetProxy', scope, rule),
  SetProxyFromURL: (scope: string, url: string) => call<Settings>('SetProxyFromURL', scope, url),
  SetAllProxies: (rule: ProxyRule) => call<Settings>('SetAllProxies', rule),
  DisableAllProxies: () => call<Settings>('DisableAllProxies'),
  TestProxy: (scope: string, probeUrl: string) => call<TestResult>('TestProxy', scope, probeUrl),
  TestProxyRule: (rule: ProxyRule, probeUrl: string) => call<TestResult>('TestProxyRule', rule, probeUrl),
  OpenDataDir: () => call<void>('OpenDataDir'),
  OpenPath: (path: string) => call<void>('OpenPath', path),
  RevealInExplorer: (path: string) => call<void>('RevealInExplorer', path),
  PickDirectory: (title: string) => call<string>('PickDirectory', title),
  PickFile: (title: string) => call<string>('PickFile', title),
  ReadTextFile: (path: string, maxBytes: number) => call<string>('ReadTextFile', path, maxBytes),
  ListLocal: (dir: string) => call<LocalListing>('ListLocal', dir),

  // ssh
  ListSSHHosts: () => call<SSHHost[]>('ListSSHHosts'),
  GetSSHHost: (id: string) => call<SSHHost>('GetSSHHost', id),
  SaveSSHHost: (h: SSHHost) => call<SSHHost>('SaveSSHHost', h),
  DeleteSSHHost: (id: string) => call<void>('DeleteSSHHost', id),
  ListSSHKeys: () => call<SSHKey[]>('ListSSHKeys'),
  GenerateSSHKey: (opts: { name: string; type: string; bits: number; comment: string; passphrase: string }) =>
    call<SSHKey>('GenerateSSHKey', opts),
  ImportSSHKey: (opts: { name: string; privateKey: string; passphrase: string; comment: string }) =>
    call<SSHKey>('ImportSSHKey', opts),
  DeleteSSHKey: (id: string) => call<void>('DeleteSSHKey', id),
  ExportSSHPrivateKey: (id: string) => call<string>('ExportSSHPrivateKey', id),
  SSHPublicKey: (id: string) => call<string>('SSHPublicKey', id),
  SaveSSHPublicKeyToFile: (id: string, target: string) => call<string>('SaveSSHPublicKeyToFile', id, target),
  ConnectSSH: (hostId: string, opts: DialOptions) => call<SSHSession>('ConnectSSH', hostId, opts),
  SSHSessions: () => call<SSHSession[]>('SSHSessions'),
  DisconnectSSH: (id: string) => call<void>('DisconnectSSH', id),
  TestSSHHost: (hostId: string, opts: DialOptions) =>
    call<{ ok: boolean; transport?: string; chain?: string[]; elapsedMs?: number; uname?: string; error?: string }>(
      'TestSSHHost',
      hostId,
      opts,
    ),
  RunSSHCommand: (sessionId: string, command: string, timeoutSec: number) =>
    call<{ stdout: string; stderr: string; exitCode: number }>('RunSSHCommand', sessionId, command, timeoutSec),
  SFTPList: (sessionId: string, dir: string) => call<RemoteFile[]>('SFTPList', sessionId, dir),
  SFTPHome: (sessionId: string) => call<string>('SFTPHome', sessionId),
  SFTPMkdir: (sessionId: string, dir: string) => call<void>('SFTPMkdir', sessionId, dir),
  SFTPRemove: (sessionId: string, path: string, recursive: boolean) =>
    call<void>('SFTPRemove', sessionId, path, recursive),
  SFTPRename: (sessionId: string, from: string, to: string) => call<void>('SFTPRename', sessionId, from, to),
  SFTPReadFile: (sessionId: string, path: string) => call<string>('SFTPReadFile', sessionId, path),
  SFTPWriteFile: (sessionId: string, path: string, content: string) =>
    call<void>('SFTPWriteFile', sessionId, path, content),
  SFTPDownload: (sessionId: string, remote: string, local: string) =>
    call<{ bytes: number; path: string }>('SFTPDownload', sessionId, remote, local),
  SFTPUpload: (sessionId: string, local: string, remote: string) =>
    call<{ bytes: number; path: string }>('SFTPUpload', sessionId, local, remote),
  OpenTerminal: (sessionId: string, cols: number, rows: number) => call<string>('OpenTerminal', sessionId, cols, rows),
  WriteTerminal: (id: string, data: string) => call<void>('WriteTerminal', id, data),
  ResizeTerminal: (id: string, cols: number, rows: number) => call<void>('ResizeTerminal', id, cols, rows),
  CloseTerminal: (id: string) => call<void>('CloseTerminal', id),
  KnownHosts: () => call<Record<string, string>>('KnownHosts'),
  TrustHostKey: (addr: string, fingerprint: string) => call<void>('TrustHostKey', addr, fingerprint),

  // ftp
  ListFTPProfiles: () => call<FTPProfile[]>('ListFTPProfiles'),
  SaveFTPProfile: (p: FTPProfile) => call<FTPProfile>('SaveFTPProfile', p),
  DeleteFTPProfile: (id: string) => call<void>('DeleteFTPProfile', id),
  ConnectFTP: (profileId: string) => call<FTPSession>('ConnectFTP', profileId),
  ConnectFTPDirect: (cfg: FTPProfile['config']) => call<FTPSession>('ConnectFTPDirect', cfg),
  FTPSessions: () => call<FTPSession[]>('FTPSessions'),
  DisconnectFTP: (id: string) => call<void>('DisconnectFTP', id),
  FTPList: (id: string, dir: string) =>
    call<{ path: string; parent: string; entries: FTPEntry[] }>('FTPList', id, dir),
  FTPMkdir: (id: string, dir: string) => call<void>('FTPMkdir', id, dir),
  FTPRemove: (id: string, target: string, isDir: boolean) => call<void>('FTPRemove', id, target, isDir),
  FTPRename: (id: string, from: string, to: string) => call<void>('FTPRename', id, from, to),
  FTPDownload: (id: string, remote: string, local: string) =>
    call<{ bytes: number; path: string }>('FTPDownload', id, remote, local),
  FTPUpload: (id: string, local: string, remote: string) =>
    call<{ bytes: number; path: string }>('FTPUpload', id, local, remote),

  // downloads
  AddDownload: (req: DownloadRequest) => call<DownloadTask>('AddDownload', req),
  AddDownloadAndStart: (req: DownloadRequest) => call<DownloadTask>('AddDownloadAndStart', req),
  StartDownload: (id: string) => call<void>('StartDownload', id),
  PauseDownload: (id: string) => call<void>('PauseDownload', id),
  CancelDownload: (id: string) => call<void>('CancelDownload', id),
  RemoveDownload: (id: string, deleteFile: boolean) => call<void>('RemoveDownload', id, deleteFile),
  ListDownloads: () => call<DownloadTask[]>('ListDownloads'),
  ClearFinishedDownloads: () => call<void>('ClearFinishedDownloads'),
  PauseAllDownloads: () => call<void>('PauseAllDownloads'),
  DownloadDefaults: () =>
    call<{ directory: string; connections: number; concurrency: number; chunkSize: number; speedLimitKBs: number }>(
      'DownloadDefaults',
    ),
  ProbeURL: (url: string) =>
    call<{ ok: boolean; filename?: string; total?: number; rangeOk?: boolean; finalUrl?: string; proxyUsed?: string; error?: string }>(
      'ProbeURL',
      url,
    ),

  // notes
  ListNotes: (includeDeleted: boolean) => call<Note[]>('ListNotes', includeDeleted),
  GetNote: (id: string) => call<Note>('GetNote', id),
  SaveNote: (n: Note) => call<Note>('SaveNote', n),
  DeleteNote: (id: string) => call<void>('DeleteNote', id),
  NoteFolders: () => call<string[]>('NoteFolders'),
  SyncNotes: () => call<SyncResult>('SyncNotes'),
  ExportNoteMarkdown: (id: string, target: string) => call<string>('ExportNoteMarkdown', id, target),

  // server
  ServerStatus: () => call<ServerStatus>('ServerStatus'),
  ConnectServer: () => call<ServerStatus>('ConnectServer'),
  SaveServerEndpoint: (host: string, port: number, preferUdp: boolean, disableUdp: boolean) =>
    call<Settings>('SaveServerEndpoint', host, port, preferUdp, disableUdp),
  RegisterServer: (username: string, password: string, email: string, displayName: string) =>
    call<ServerStatus>('RegisterServer', username, password, email, displayName),
  LoginServer: (username: string, password: string) => call<ServerStatus>('LoginServer', username, password),
  LogoutServer: () => call<void>('LogoutServer'),
  ChangeServerPassword: (oldPassword: string, newPassword: string) =>
    call<Record<string, unknown>>('ChangeServerPassword', oldPassword, newPassword),
  RemoteServerInfo: () => call<Record<string, unknown>>('RemoteServerInfo'),
  RefreshServerUser: () => call<ServerUser>('RefreshServerUser'),
  ListServerFiles: (path: string) => call<RemoteEntry[]>('ListServerFiles', path),
  MkdirServerFile: (path: string) => call<void>('MkdirServerFile', path),
  RemoveServerFile: (path: string, recursive: boolean) => call<void>('RemoveServerFile', path, recursive),
  RenameServerFile: (from: string, to: string) => call<void>('RenameServerFile', from, to),
  ServerUsage: () => call<{ usedBytes: number; quotaBytes: number }>('ServerUsage'),
  UploadToServer: (localPaths: string[], remoteDir: string) =>
    call<{ results: Array<Record<string, unknown>> }>('UploadToServer', localPaths, remoteDir),
  DownloadFromServer: (remotePath: string, localDir: string) =>
    call<{ bytes: number; path: string }>('DownloadFromServer', remotePath, localDir),
  RelayStatus: () => call<{ available: boolean; detail: string }>('RelayStatus'),

  // netdisk
  NetdiskProviders: () => call<NetdiskProvider[]>('NetdiskProviders'),
  ListNetdiskAccounts: () => call<NetdiskAccount[]>('ListNetdiskAccounts'),
  AddNetdiskAccount: (provider: string, cookie: string, displayName: string) =>
    call<NetdiskAccount>('AddNetdiskAccount', provider, cookie, displayName),
  UpdateNetdiskCookie: (accountId: string, cookie: string) =>
    call<NetdiskAccount>('UpdateNetdiskCookie', accountId, cookie),
  UpdateNetdiskRemark: (accountId: string, remark: string) =>
    call<NetdiskAccount>('UpdateNetdiskRemark', accountId, remark),
  RefreshNetdiskAccount: (accountId: string) => call<NetdiskAccount>('RefreshNetdiskAccount', accountId),
  RemoveNetdiskAccount: (accountId: string) => call<void>('RemoveNetdiskAccount', accountId),
  NetdiskListFiles: (req: { account_id: string; path?: string; parent_id?: string; page?: number; page_size?: number }) =>
    call<NetdiskFileList>('NetdiskListFiles', req),
  NetdiskSearch: (req: { account_id: string; path?: string; query: string; page?: number; page_size?: number }) =>
    call<NetdiskFileList>('NetdiskSearch', req),
  NetdiskMkdir: (req: { account_id: string; parent_id?: string; path?: string; name: string }) =>
    call<void>('NetdiskMkdir', req),
  NetdiskRename: (req: { account_id: string; file_id: string; path?: string; new_name: string }) =>
    call<void>('NetdiskRename', req),
  NetdiskDelete: (req: { account_id: string; file_ids?: string[]; paths?: string[] }) =>
    call<void>('NetdiskDelete', req),
  NetdiskUpload: (req: {
    account_id: string
    local_path: string
    name?: string
    target_path?: string
    target_parent_id?: string
    overwrite?: boolean
  }) => call<{ file_id?: string; name: string; path?: string; size: number }>('NetdiskUpload', req),
  NetdiskResolveLink: (req: {
    account_id: string
    file_id: string
    name: string
    path: string
    size: number
    md5?: string
    token_accelerate?: boolean
  }) => call<{ url: string; filename: string; size: number; headers?: Record<string, string>; cookie?: string }>(
    'NetdiskResolveLink',
    req,
  ),
  NetdiskEnqueueDownload: (req: {
    account_id: string
    file_id: string
    name: string
    path: string
    size: number
    md5?: string
    token_accelerate?: boolean
  }) => call<DownloadTask>('NetdiskEnqueueDownload', req),
  NetdiskCreateShare: (req: {
    account_id: string
    file_ids: string[]
    title?: string
    password?: string
    expire_days?: number
  }) => call<{ share_id?: string; url: string; password?: string; expire_days?: number }>('NetdiskCreateShare', req),
  NetdiskShares: (accountId: string, page: number, pageSize: number) =>
    call<any>('NetdiskShares', accountId, page, pageSize),
  NetdiskCancelShares: (accountId: string, shareIds: string[]) =>
    call<void>('NetdiskCancelShares', accountId, shareIds),
  NetdiskRecycle: (accountId: string, page: number, pageSize: number) =>
    call<any>('NetdiskRecycle', accountId, page, pageSize),
  NetdiskRestoreRecycle: (accountId: string, itemIds: string[]) =>
    call<void>('NetdiskRestoreRecycle', accountId, itemIds),
  NetdiskDeleteRecycle: (accountId: string, itemIds: string[]) =>
    call<void>('NetdiskDeleteRecycle', accountId, itemIds),
  NetdiskVideoPreview: (req: { account_id: string; file_id: string; path?: string; name?: string; size?: number }) =>
    call<{ filename?: string; size?: number; default_quality?: string; qualities: Array<{ key: string; label: string; url: string }> }>(
      'NetdiskVideoPreview',
      req,
    ),

  // proxy chain, rules, geoip, latency, media, hotkey
  GetProxyChain: () => call<ProxyChainSettings>('GetProxyChain'),
  SaveProxyChain: (cfg: ProxyChainSettings) => call<ProxyChainSettings>('SaveProxyChain', cfg),
  GetDirectRules: () => call<DirectRuleSettings>('GetDirectRules'),
  SaveDirectRules: (cfg: DirectRuleSettings) => call<DirectRuleSettings>('SaveDirectRules', cfg),
  GeoIPStatus: () => call<GeoIPStatus>('GeoIPStatus'),
  DownloadGeoIP: () => call<GeoIPStatus>('DownloadGeoIP'),
  GeoIPCountries: () => call<string[]>('GeoIPCountries'),
  ScopeInternalProxy: (scope: string) => call<boolean>('ScopeInternalProxy', scope),
  SetScopeInternalProxy: (scope: string, use: boolean) => call<Settings>('SetScopeInternalProxy', scope, use),
  LoadLatencyResults: () => call<Record<string, LatencyEntry>>('LoadLatencyResults'),
  SaveLatencyResults: (entries: LatencyEntry[]) => call<void>('SaveLatencyResults', entries),
  RegisterMedia: (path: string) => call<string>('RegisterMedia', path),
  PreviewCloudMedia: (path: string) => call<string>('PreviewCloudMedia', path),
  PreviewSFTPMedia: (sessionId: string, path: string) => call<string>('PreviewSFTPMedia', sessionId, path),
  SaveScreenshotHotkey: (combo: string) => call<string>('SaveScreenshotHotkey', combo),
  HotkeySupported: () => call<boolean>('HotkeySupported'),

  // transfers
  ListTransfers: () => call<TransferTask[]>('ListTransfers'),
  PauseTransfer: (id: string) => call<void>('PauseTransfer', id),
  ResumeTransfer: (id: string) => call<void>('ResumeTransfer', id),
  CancelTransfer: (id: string) => call<void>('CancelTransfer', id),
  RemoveTransfer: (id: string) => call<void>('RemoveTransfer', id),
  SyncSubscriptions: () => call<SubSyncResult>('SyncSubscriptions'),
  ExtractCloudFile: (path: string) => call<Record<string, unknown>>('ExtractCloudFile', path),
  SaveLastLocalDir: (dir: string) => call<void>('SaveLastLocalDir', dir),
  LastLocalDir: () => call<string>('LastLocalDir'),

  // proxy engine
  SubscriptionNodes: (sourceId: string) => call<MiproxyNode[]>('SubscriptionNodes', sourceId),
  ActivateProxyNode: (node: MiproxyNode) => call<Record<string, unknown>>('ActivateProxyNode', node),
  TestAllNodes: (nodes: MiproxyNode[]) => call<NodeTestResult[]>('TestAllNodes', nodes),
  SetProxyMode: (mode: string) => call<Settings>('SetProxyMode', mode),
  SetSystemProxy: (enabled: boolean) => call<Record<string, unknown>>('SetSystemProxy', enabled),
  SystemProxyState: () =>
    call<{ enabled: boolean; server: string; supported: boolean; setting: boolean }>('SystemProxyState'),
  ActiveProxyNode: () => call<ActiveProxyInfo>('ActiveProxyNode'),
  DeactivateProxy: () => call<Settings>('DeactivateProxy'),
  ProxyRuntimeStatus: () => call<ProxyRuntimeStatus>('ProxyRuntimeStatus'),
  TestProxyNode: (node: MiproxyNode) =>
    call<{ ok: boolean; latencyMs?: number; error?: string }>('TestProxyNode', node),
  ListCustomNodes: () => call<MiproxyNode[]>('ListCustomNodes'),
  SaveCustomNode: (node: MiproxyNode) => call<MiproxyNode>('SaveCustomNode', node),
  DeleteCustomNode: (id: string) => call<void>('DeleteCustomNode', id),
  ImportNodeLinks: (text: string) => call<MiproxyNode[]>('ImportNodeLinks', text),
  ParseNodeLinks: (text: string) => call<MiproxyNode[]>('ParseNodeLinks', text),
  GetProxyRuntimeSettings: () => call<ProxyRuntimeSettings>('GetProxyRuntimeSettings'),
  SaveProxyRuntimeSettings: (next: ProxyRuntimeSettings) =>
    call<ProxyRuntimeSettings>('SaveProxyRuntimeSettings', next),
  // subscriptions
  ListSubscriptions: () => call<SubscriptionSource[]>('ListSubscriptions'),
  SaveSubscription: (src: Partial<SubscriptionSource>) => call<SubscriptionSource>('SaveSubscription', src),
  DeleteSubscription: (id: string) => call<void>('DeleteSubscription', id),
  RefreshSubscription: (id: string) => call<SubscriptionSource>('RefreshSubscription', id),
  RefreshAllSubscriptions: () => call<any[]>('RefreshAllSubscriptions'),
  ApplySubscriptionNode: (sourceId: string, nodeIndex: number, scopes: string[], all: boolean) =>
    call<Settings>('ApplySubscriptionNode', sourceId, nodeIndex, scopes, all),
  TestSubscriptionNode: (sourceId: string, nodeIndex: number) =>
    call<TestResult>('TestSubscriptionNode', sourceId, nodeIndex),

  // screenshots
  CaptureScreenshot: (display: number) => call<ScreenshotItem>('CaptureScreenshot', display),
  ScreenshotDisplays: () => call<number>('ScreenshotDisplays'),
  ListScreenshots: (limit: number) => call<ScreenshotItem[]>('ListScreenshots', limit),
  DeleteScreenshot: (path: string) => call<void>('DeleteScreenshot', path),
  GetScreenshotSettings: () => call<ScreenshotSettings>('GetScreenshotSettings'),
  SaveScreenshotSettings: (next: ScreenshotSettings) => call<ScreenshotSettings>('SaveScreenshotSettings', next),
  SetClipboardWatch: (enabled: boolean) => call<boolean>('SetClipboardWatch', enabled),
  ClipboardWatching: () => call<boolean>('ClipboardWatching'),
  OpenScreenshotDirectory: () => call<void>('OpenScreenshotDirectory'),
  PickScreenshotDirectory: () => call<string>('PickScreenshotDirectory'),
  ScreenshotDefaultDir: () => call<string>('ScreenshotDefaultDir'),
  ScreenshotThumbnail: (path: string, maxSize: number) => call<string>('ScreenshotThumbnail', path, maxSize),

  // updates
  GetUpdateSettings: () => call<UpdateSettings>('GetUpdateSettings'),
  SaveUpdateSettings: (next: UpdateSettings) => call<UpdateSettings>('SaveUpdateSettings', next),
  CheckUpdate: () => call<UpdateStatus>('CheckUpdate'),
  DownloadUpdate: (rel: UpdateRelease) => call<string>('DownloadUpdate', rel),
  ApplyUpdate: (stagedPath: string) => call<string>('ApplyUpdate', stagedPath),
  UpdateCurrentVersion: () => call<string>('UpdateCurrentVersion'),
  UploadCurrentBuild: (channel: string, notes: string, mandatory: boolean) =>
    call<Record<string, unknown>>('UploadCurrentBuild', channel, notes, mandatory),
  ListServerReleases: (platform: string, channel: string) =>
    call<UpdateRelease[]>('ListServerReleases', platform, channel),
  DeleteServerRelease: (id: string) => call<void>('DeleteServerRelease', id),
  ProbeGitHub: () => call<Record<string, unknown>>('ProbeGitHub'),

  // autostart
  GetAutostartStatus: () => call<AutostartStatus>('GetAutostartStatus'),
  SetAutostart: (enabled: boolean) => call<AutostartStatus>('SetAutostart', enabled),

  // system tray
  TrayAvailable: () => call<boolean>('TrayAvailable'),
  TrayEnabled: () => call<boolean>('TrayEnabled'),
  SetTrayEnabled: (enabled: boolean) => call<StartupSettings>('SetTrayEnabled', enabled),
  SetCloseToTray: (enabled: boolean) => call<StartupSettings>('SetCloseToTray', enabled),
  ShowMainWindow: () => call<void>('ShowMainWindow'),
  HideMainWindow: () => call<void>('HideMainWindow'),
}

/**
 * Go marshals a nil slice as JSON `null`, and empty lists are extremely common
 * (no hosts yet, no sessions, no downloads). Normalise at the bridge so every
 * page can rely on arrays instead of guarding each call.
 */
const ARRAY_METHODS = [
  'ListSSHHosts',
  'ListSSHKeys',
  'SSHSessions',
  'ListFTPProfiles',
  'FTPSessions',
  'ListDownloads',
  'ListNotes',
  'NoteFolders',
  'ProxyScopes',
  'ListNetdiskAccounts',
  'NetdiskProviders',
  'ListServerFiles',
  'ListSubscriptions',
  'ListTransfers',
  'GetProxyChain',
  'GetDirectRules',
  'GeoIPStatus',
  'GeoIPCountries',
  'LoadLatencyResults',
  'HotkeySupported',
  'SubscriptionNodes',
  'ListCustomNodes',
  'ListScreenshots',
  'ListServerReleases',
] as const

function coerceList(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

const api = { ...rawApi } as typeof rawApi

for (const method of ARRAY_METHODS) {
  const original = (api as any)[method] as (...args: any[]) => Promise<unknown>
  ;(api as any)[method] = async (...args: any[]) => coerceList(await original(...args))
}

// List-shaped responses that nest the array inside an object.
const NESTED_LISTS: Array<[keyof typeof rawApi, string]> = [
  ['SFTPList', 'array'],
  ['FTPList', 'entries'],
  ['ListLocal', 'entries'],
  ['ListServerFiles', 'array'],
  ['NetdiskListFiles', 'files'],
  ['NetdiskSearch', 'files'],
]

for (const [method, field] of NESTED_LISTS) {
  const original = (api as any)[method] as (...args: any[]) => Promise<any>
  ;(api as any)[method] = async (...args: any[]) => {
    const value = await original(...args)
    if (field === 'array') return coerceList(value)
    if (value && typeof value === 'object') {
      return { ...value, [field]: coerceList((value as any)[field]) }
    }
    return value
  }
}

export { api }
export default api











