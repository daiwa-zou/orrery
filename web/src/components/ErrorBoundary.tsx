import { Component, type ReactNode } from 'react'
import { Button } from './primitives'

interface State {
  error?: Error
}

/**
 * Last line of defence: a render-time throw anywhere below takes down only
 * this subtree, not the whole dashboard. For an incident-response tool, a
 * white screen during an incident is the worst possible failure mode.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = {}

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  render() {
    if (!this.state.error) return this.props.children

    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 p-8 text-center">
        <h1 className="text-base font-semibold text-ink">Something broke in the UI</h1>
        <p className="max-w-md font-mono text-xs break-words text-ink-faint">
          {this.state.error.message}
        </p>
        <div className="flex gap-2">
          <Button onClick={() => this.setState({ error: undefined })}>Try again</Button>
          <Button variant="primary" onClick={() => window.location.reload()}>
            Reload
          </Button>
        </div>
      </div>
    )
  }
}
