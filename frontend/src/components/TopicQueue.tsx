import { CheckCircle, Circle, Spinner, Warning } from '@phosphor-icons/react'
import type { Session, SessionStatus } from '@/lib/types'

interface TopicQueueProps {
  topics: Session[]
  currentTopicId: string | null
}

const statusBadge: Record<SessionStatus, string> = {
  pending: 'bg-muted/40 text-muted-foreground ring-border/50',
  running: 'bg-violet-500/15 text-violet-300 ring-violet-500/40',
  done: 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/40',
  error: 'bg-red-500/15 text-red-300 ring-red-500/40',
}

function StatusIcon({ status }: { status: SessionStatus }) {
  switch (status) {
    case 'running':
      return (
        <Spinner
          weight="bold"
          className="h-3.5 w-3.5 text-violet-300 animate-spin"
        />
      )
    case 'done':
      return (
        <CheckCircle
          weight="duotone"
          className="h-3.5 w-3.5 text-emerald-300"
        />
      )
    case 'error':
      return <Warning weight="duotone" className="h-3.5 w-3.5 text-red-300" />
    default:
      return (
        <Circle
          weight="duotone"
          className="h-3.5 w-3.5 text-muted-foreground"
        />
      )
  }
}

export function TopicQueue({ topics, currentTopicId }: TopicQueueProps) {
  if (topics.length <= 1) return null
  return (
    <div className="flex items-center gap-1.5 overflow-x-auto py-1">
      {topics.map((t, i) => {
        const isCurrent = t.id === currentTopicId
        const badgeClass = statusBadge[t.status] ?? statusBadge.pending
        return (
          <span
            key={t.id}
            title={t.title}
            className={`inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1 transition-all ${badgeClass} ${
              isCurrent ? 'ring-2 ring-offset-1 ring-offset-background' : ''
            }`}
          >
            <StatusIcon status={t.status} />
            <span className="text-foreground/40 tabular-nums">{i + 1}.</span>
            <span className="max-w-[180px] truncate">{t.title}</span>
          </span>
        )
      })}
    </div>
  )
}
