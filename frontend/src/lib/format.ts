export function formatBytes(n: number | undefined | null, digits = 1): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '—'
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(digits)} ${units[i]}`
}

export function formatSpeed(bps: number | undefined | null): string {
  if (!bps || bps <= 0) return '—'
  return `${formatBytes(bps)}/s`
}

export function formatTime(input?: string | number | null): string {
  if (!input) return '—'
  const d = typeof input === 'number' ? new Date(input) : new Date(input)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function relativeTime(input?: string | number | null): string {
  if (!input) return ''
  const t = typeof input === 'number' ? input : Date.parse(input)
  if (Number.isNaN(t)) return ''
  const diff = Date.now() - t
  if (diff < 60_000) return '刚刚'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`
  if (diff < 30 * 86_400_000) return `${Math.floor(diff / 86_400_000)} 天前`
  return formatTime(t)
}

export function percent(done: number, total: number): number {
  if (!total || total <= 0) return 0
  return Math.min(100, Math.max(0, (done / total) * 100))
}

export function basename(p: string): string {
  if (!p) return ''
  const norm = p.replace(/\\/g, '/').replace(/\/+$/, '')
  const i = norm.lastIndexOf('/')
  return i >= 0 ? norm.slice(i + 1) : norm
}

export function dirname(p: string): string {
  if (!p) return ''
  const norm = p.replace(/\\/g, '/')
  const i = norm.lastIndexOf('/')
  return i > 0 ? norm.slice(0, i) : norm
}

export function joinPath(base: string, name: string): string {
  if (!base) return name
  if (base.includes('\\')) {
    return base.endsWith('\\') ? base + name : `${base}\\${name}`
  }
  return base.endsWith('/') ? base + name : `${base}/${name}`
}
