function fmtMs(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) ms = 0
  const total = Math.floor(ms / 1000)
  const s = total % 60
  const m = Math.floor(total / 60) % 60
  const h = Math.floor(total / 3600)
  const pad = (n: number) => n.toString().padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

interface StatusBarProps {
  phase: string
  elapsedMs: number
  remainingMs: number
  status: string
}

export function StatusBar({ phase, elapsedMs, remainingMs, status }: StatusBarProps) {
  const total = elapsedMs + remainingMs
  return (
    <header className="flex items-center gap-6 px-4 py-2 bg-card border-b border-border text-xs text-muted-foreground">
      <span>phase: {phase}</span>
      <span>
        {fmtMs(elapsedMs)} / {fmtMs(total)}
      </span>
      <span className="ml-auto italic">{status}</span>
    </header>
  )
}
