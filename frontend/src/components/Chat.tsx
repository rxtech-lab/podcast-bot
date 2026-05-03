import { useEffect, useRef, useState } from 'react'
import { ChatCircleDots, PaperPlaneTilt } from '@phosphor-icons/react'
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
    <aside className="w-full md:w-[400px] md:flex-shrink-0 flex-1 md:flex-none flex flex-col bg-card/70 backdrop-blur-md border border-border/50 rounded-2xl overflow-hidden min-h-0 shadow-2xl shadow-black/40">
      <header className="flex items-center gap-2.5 px-4 py-3 border-b border-border/40 bg-gradient-to-b from-card/40 to-transparent">
        <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary/15 ring-1 ring-primary/30">
          <ChatCircleDots
            weight="duotone"
            className="h-4 w-4 text-primary"
          />
        </div>
        <div className="flex flex-col leading-tight">
          <span className="text-sm font-semibold">chat</span>
          <span className="text-[10px] text-muted-foreground">
            {history.length}{' '}
            {history.length === 1 ? 'message' : 'messages'}
          </span>
        </div>
      </header>

      <div ref={scrollRef} className="flex-1 overflow-y-auto px-3 py-4">
        {history.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-3 text-center px-6">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-muted/40 ring-1 ring-border/60">
              <ChatCircleDots
                weight="duotone"
                className="h-7 w-7 text-muted-foreground/60"
              />
            </div>
            <div className="space-y-1">
              <p className="text-sm font-medium text-foreground/80">
                no messages yet
              </p>
              <p className="text-xs text-muted-foreground max-w-[240px]">
                the panel will start speaking when the debate begins. jump
                in any time.
              </p>
            </div>
          </div>
        ) : (
          <ol className="m-0 p-0 list-none flex flex-col gap-3">
            {history.map((line, i) => (
              <ChatMessage key={i} line={line} />
            ))}
          </ol>
        )}
      </div>

      <form
        onSubmit={onSubmit}
        className="flex items-center gap-2 p-3 border-t border-border/40 bg-card/40"
      >
        <Input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          maxLength={500}
          autoComplete="off"
          placeholder="say something… (/end to wrap up)"
          className="flex-1"
        />
        <Button
          type="submit"
          size="icon"
          disabled={!draft.trim()}
          aria-label="send message"
        >
          <PaperPlaneTilt weight="bold" className="h-4 w-4" />
        </Button>
      </form>
    </aside>
  )
}
