import type { ChatLine } from '@/lib/types'

function speakerLabel({ speaker, role }: ChatLine): string {
  switch (role) {
    case 'host':
      return 'host'
    case 'affirmative':
      return `affirmative — ${speaker}`
    case 'negative':
      return `negative — ${speaker}`
    case 'judge':
      return 'judge'
    case 'viewer':
      return `viewer — ${speaker}`
    case 'user':
      return 'audience'
    default:
      return speaker || '?'
  }
}

const roleColor: Record<string, string> = {
  host: 'text-[var(--role-host)]',
  affirmative: 'text-[var(--role-affirmative)]',
  negative: 'text-[var(--role-negative)]',
  judge: 'text-[var(--role-judge)]',
  viewer: 'text-[var(--role-viewer)]',
  user: 'text-[var(--role-user)]',
}

export function ChatMessage({ line }: { line: ChatLine }) {
  const color = roleColor[line.role] ?? 'text-foreground'
  return (
    <li className="break-words text-sm leading-relaxed">
      <span className={`font-bold mr-1.5 ${color}`}>
        {speakerLabel(line)}:
      </span>
      <span>{line.text}</span>
    </li>
  )
}
