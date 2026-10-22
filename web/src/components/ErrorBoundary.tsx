import { Component, type ReactNode } from "react";

// Shows a crashed page's error instead of a blank screen. Keyed by the
// route, so moving to another page tries again.
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="rounded-xl border border-destructive/30 bg-destructive/10 p-5 text-sm">
        <p className="font-medium text-destructive">This page hit an error.</p>
        <p className="mt-1 font-mono text-xs break-all text-muted-foreground">{this.state.error.message}</p>
        <button className="mt-3 cursor-pointer text-brand hover:underline" onClick={() => location.reload()}>
          Reload
        </button>
      </div>
    );
  }
}
