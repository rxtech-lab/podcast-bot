import {
  Microphone,
  Gavel,
  Eye,
  User,
  ThumbsUp,
  ThumbsDown,
  LinkSimple,
  type Icon,
} from '@phosphor-icons/react'
import type { ChatLine, SourceRef } from '@/lib/types'

function sourceLabel(s: SourceRef): string {
  if (s.title) return s.title
  try {
    return new URL(s.url).host
  } catch {
    return s.url
  }
}

function speakerName({ speaker, role }: ChatLine, localUsername?: string): string {
  switch (role) {
    case 'host':
      return 'host'
    case 'judge':
      return 'judge'
    case 'user':
      // Multiple viewers can be in the same channel; only label the message
      // as "you" when its speaker matches this browser's stored username.
      if (localUsername && speaker === localUsername) return 'you'
      return speaker || 'viewer'
    default:
      return speaker || '?'
  }
}

function roleSubtitle(role: string): string | null {
  switch (role) {
    case 'affirmative':
      return 'affirmative'
    case 'negative':
      return 'negative'
    case 'viewer':
      return 'audience'
    default:
      return null
  }
}

interface RoleConfig {
  text: string
  bubble: string
  ring: string
  icon: Icon | null
}

const roleConfig: Record<string, RoleConfig> = {
  host: {
    text: 'text-sky-300',
    bubble: 'bg-sky-500/[0.07] border-sky-500/20',
    ring: 'bg-sky-500/15 ring-sky-500/30',
    icon: Microphone,
  },
  affirmative: {
    text: 'text-emerald-300',
    bubble: 'bg-emerald-500/[0.07] border-emerald-500/20',
    ring: 'bg-emerald-500/15 ring-emerald-500/30',
    icon: ThumbsUp,
  },
  negative: {
    text: 'text-rose-300',
    bubble: 'bg-rose-500/[0.07] border-rose-500/20',
    ring: 'bg-rose-500/15 ring-rose-500/30',
    icon: ThumbsDown,
  },
  judge: {
    text: 'text-amber-300',
    bubble: 'bg-amber-500/[0.07] border-amber-500/20',
    ring: 'bg-amber-500/15 ring-amber-500/30',
    icon: Gavel,
  },
  viewer: {
    text: 'text-violet-300',
    bubble: 'bg-violet-500/[0.07] border-violet-500/20',
    ring: 'bg-violet-500/15 ring-violet-500/30',
    icon: Eye,
  },
  user: {
    text: 'text-primary',
    bubble: 'bg-primary/[0.10] border-primary/30',
    ring: 'bg-primary/20 ring-primary/40',
    icon: User,
  },
}

const fixedIconRoles = new Set(['host', 'judge', 'user'])

export function ChatMessage({
  line,
  localUsername,
}: {
  line: ChatLine
  localUsername?: string
}) {
  const cfg = roleConfig[line.role] ?? roleConfig.user
  // Right-align bubbles only for the local viewer's own messages, so other
  // viewers' messages render on the left like every other speaker.
  const isMine = line.role === 'user' && !!localUsername && line.speaker === localUsername
  // Use the generic person icon for the local viewer (their identity is
  // implicit "you"); other viewers get their initial so they're distinguishable.
  const useIcon = fixedIconRoles.has(line.role) && (line.role !== 'user' || isMine)
  const Icon = cfg.icon
  const initial = (line.speaker || '?').charAt(0).toUpperCase()
  const subtitle = roleSubtitle(line.role)

  return (
    <li className={`flex gap-2.5 ${isMine ? 'flex-row-reverse' : ''}`}>
      <div
        className={`flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full text-xs font-bold ring-1 ${cfg.ring} ${cfg.text}`}
      >
        {useIcon && Icon ? <Icon weight="bold" className="h-4 w-4" /> : initial}
      </div>
      <div
        className={`min-w-0 flex flex-col gap-1 ${isMine ? 'items-end' : 'items-start'} max-w-[85%]`}
      >
        <div className="flex items-baseline gap-1.5 px-1 text-[11px] leading-none">
          <span className={`font-semibold ${cfg.text}`}>
            {speakerName(line, localUsername)}
          </span>
          {subtitle && (
            <span className="text-muted-foreground/80">· {subtitle}</span>
          )}
        </div>
        <div
          className={`rounded-2xl border px-3 py-2 text-sm leading-snug whitespace-pre-wrap break-words ${cfg.bubble}`}
        >
          {line.text}
        </div>
        {line.judgementComment && (
          <div className="flex items-start gap-1.5 rounded-xl border border-amber-500/20 bg-amber-500/[0.07] px-3 py-2 text-xs leading-snug text-amber-300">
            <Gavel weight="bold" className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
            <span>
              <span className="font-semibold">judgement</span> ·{' '}
              {line.judgementComment}
            </span>
          </div>
        )}
        {line.sources && line.sources.length > 0 && (
          <div className="flex flex-wrap gap-1 px-1">
            {line.sources.map((s) => (
              <a
                key={s.url}
                href={s.url}
                target="_blank"
                rel="noopener noreferrer"
                title={s.snippet || s.url}
                className="inline-flex max-w-[220px] items-center gap-1 rounded-full border border-white/10 bg-white/[0.04] px-2 py-0.5 text-[11px] leading-tight text-muted-foreground hover:text-foreground hover:border-white/25"
              >
                <LinkSimple weight="bold" className="h-3 w-3 flex-shrink-0" />
                <span className="truncate">{sourceLabel(s)}</span>
              </a>
            ))}
          </div>
        )}
      </div>
    </li>
  )
}
