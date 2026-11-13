import React, { createContext, useCallback, useContext, useMemo, useState } from 'react'

/* ---------------- toasts ---------------- */

type ToastKind = 'info' | 'error' | 'warn'
interface ToastItem {
  id: number
  message: string
  kind: ToastKind
}

interface ToastApi {
  push: (message: string, kind?: ToastKind) => void
  error: (err: unknown) => void
}

const ToastCtx = createContext<ToastApi>({ push: () => {}, error: () => {} })

export function useToast(): ToastApi {
  return useContext(ToastCtx)
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])

  const push = useCallback((message: string, kind: ToastKind = 'info') => {
    const id = Date.now() + Math.random()
    setItems((prev) => {
      // Collapse identical messages fired in quick succession.
      const last = prev[prev.length - 1]
      if (last && last.message === message && last.kind === kind) {
        return prev
      }
      return [...prev, { id, message, kind }].slice(-5)
    })
    window.setTimeout(() => {
      setItems((prev) => prev.filter((t) => t.id !== id))
    }, kind === 'error' ? 6000 : 3200)
  }, [])

  const error = useCallback(
    (err: unknown) => {
      const msg = err instanceof Error ? err.message : typeof err === 'string' ? err : JSON.stringify(err)
      push(msg, 'error')
    },
    [push],
  )

  const value = useMemo(() => ({ push, error }), [push, error])

  return (
    <ToastCtx.Provider value={value}>
      {children}
      <div className="toast-wrap">
        {items.map((t) => (
          <div key={t.id} className={`toast ${t.kind === 'info' ? '' : t.kind}`}>
            {t.message}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}

/* ---------------- modal ---------------- */

export function Modal({
  title,
  children,
  onClose,
  footer,
  wide,
}: {
  title: React.ReactNode
  children: React.ReactNode
  onClose: () => void
  footer?: React.ReactNode
  wide?: boolean
}) {
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? 'wide' : ''}`}>
        <div className="modal-head">
          <span style={{ flex: 1 }}>{title}</span>
          <button className="btn btn-ghost btn-sm" onClick={onClose} title="关闭">
            ✕
          </button>
        </div>
        <div className="modal-body">{children}</div>
        {footer ? <div className="modal-foot">{footer}</div> : null}
      </div>
    </div>
  )
}

/* ---------------- small primitives ---------------- */

export function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {hint ? <span className="hint">{hint}</span> : null}
    </div>
  )
}

export function Switch({ checked, onChange, title }: { checked: boolean; onChange: (v: boolean) => void; title?: string }) {
  return (
    <button
      type="button"
      title={title}
      className={`switch ${checked ? 'on' : ''}`}
      onClick={() => onChange(!checked)}
      aria-pressed={checked}
    />
  )
}

export function Progress({ percent, indeterminate }: { percent: number; indeterminate?: boolean }) {
  return (
    <div className="progress">
      <div
        className="progress-fill"
        style={{
          width: indeterminate ? '35%' : `${Math.max(0, Math.min(100, percent))}%`,
          opacity: indeterminate ? 0.7 : 1,
        }}
      />
    </div>
  )
}

export function Empty({ icon, title, hint }: { icon?: string; title: string; hint?: string }) {
  return (
    <div className="empty">
      <div className="empty-icon">{icon ?? '◇'}</div>
      <div>{title}</div>
      {hint ? <div className="small faint">{hint}</div> : null}
    </div>
  )
}

export function Stat({ label, value, sub }: { label: string; value: React.ReactNode; sub?: React.ReactNode }) {
  return (
    <div className="stat">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      {sub ? <div className="sub">{sub}</div> : null}
    </div>
  )
}

export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: Array<{ id: T; label: string }>
  value: T
  onChange: (v: T) => void
}) {
  return (
    <div className="tabs">
      {tabs.map((t) => (
        <button key={t.id} className={`tab ${value === t.id ? 'active' : ''}`} onClick={() => onChange(t.id)}>
          {t.label}
        </button>
      ))}
    </div>
  )
}

export function Tag({ kind = 'default', children }: { kind?: 'default' | 'ok' | 'warn' | 'bad' | 'info'; children: React.ReactNode }) {
  const cls = kind === 'default' ? 'tag' : `tag tag-${kind}`
  return <span className={cls}>{children}</span>
}

export function Spinner({ label }: { label?: string }) {
  return (
    <span className="row" style={{ gap: 8 }}>
      <span
        style={{
          width: 14,
          height: 14,
          borderRadius: '50%',
          border: '2px solid var(--border-strong)',
          borderTopColor: 'var(--accent)',
          display: 'inline-block',
          animation: 'spin 0.7s linear infinite',
        }}
      />
      {label ? <span className="small dim">{label}</span> : null}
      <style>{`@keyframes spin { to { transform: rotate(360deg) } }`}</style>
    </span>
  )
}

export function Confirm({
  title,
  message,
  onCancel,
  onConfirm,
  danger,
}: {
  title: string
  message: React.ReactNode
  onCancel: () => void
  onConfirm: () => void
  danger?: boolean
}) {
  return (
    <Modal
      title={title}
      onClose={onCancel}
      footer={
        <>
          <button className="btn" onClick={onCancel}>
            取消
          </button>
          <button className={`btn ${danger ? 'btn-danger' : 'btn-primary'}`} onClick={onConfirm}>
            确认
          </button>
        </>
      }
    >
      <div className="dim">{message}</div>
    </Modal>
  )
}
