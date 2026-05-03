import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ChatMessage } from '@/components/ChatMessage'
import { sendMessage } from '@/lib/api'
import type { ChatLine } from '@/lib/types'

export function Chat({ history }: { history: ChatLine[] }) {
  const [draft, setDraft] = useState('')
  const scrollRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [history.length])

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const text = draft.trim()
    if (!text) return
    setDraft('')
    try {
      await sendMessage(text)
    } catch (err) {
      console.warn('send failed', err)
    }
  }

  return (
    <aside className="w-full md:w-[360px] md:flex-shrink-0 flex-1 md:flex-none flex flex-col bg-card border-t md:border-t-0 md:border-l border-border min-h-0">
      <h2 className="m-0 px-4 py-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground border-b border-border">
        chat
      </h2>
      <div ref={scrollRef} className="flex-1 overflow-y-auto px-4 py-3">
        <ol className="m-0 p-0 list-none flex flex-col gap-2">
          {history.map((line, i) => (
            <ChatMessage key={i} line={line} />
          ))}
        </ol>
      </div>
      <form
        onSubmit={onSubmit}
        className="flex gap-1.5 p-2 border-t border-border"
      >
        <Input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          maxLength={500}
          autoComplete="off"
          placeholder="say something to the panel… (/end to wrap up)"
          className="flex-1"
        />
        <Button type="submit">send</Button>
      </form>
    </aside>
  )
}
