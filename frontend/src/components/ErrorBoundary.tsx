import React from 'react'

interface State {
  error: Error | null
  info: string
}

/**
 * Catches render-time failures so a single broken page cannot blank the whole
 * window; the error is shown in place with a retry button.
 */
export class ErrorBoundary extends React.Component<{ children: React.ReactNode; label?: string }, State> {
  state: State = { error: null, info: '' }

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // Keep the detail for the on-screen report; also log for devtools.
    console.error('page render failed', error, info.componentStack)
    this.setState({ info: info.componentStack ?? '' })
  }

  private reset = () => this.setState({ error: null, info: '' })

  render() {
    const { error, info } = this.state
    if (!error) return this.props.children
    return (
      <div className="card" style={{ borderColor: 'var(--danger)' }}>
        <h3 className="card-title" style={{ color: 'var(--danger)' }}>
          页面渲染失败{this.props.label ? `：${this.props.label}` : ''}
        </h3>
        <p className="card-desc">该页面在渲染时抛出异常，其余功能不受影响。</p>
        <pre
          className="mono"
          style={{
            whiteSpace: 'pre-wrap',
            maxHeight: 260,
            overflow: 'auto',
            background: 'var(--bg-sunken)',
            padding: 12,
            borderRadius: 8,
            color: 'var(--text-dim)',
          }}
        >
          {error.message}
          {info ? `\n${info}` : ''}
        </pre>
        <div className="row" style={{ marginTop: 12 }}>
          <button className="btn btn-primary btn-sm" onClick={this.reset}>
            重试渲染
          </button>
        </div>
      </div>
    )
  }
}

export default ErrorBoundary
