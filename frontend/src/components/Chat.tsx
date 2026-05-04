import { useEffect, useRef, useState } from 'react'
import { ChatCircleDots, PaperPlaneTilt, User } from '@phosphor-icons/react'
import { Button } from '@/components/ui/button'
import { ChatMessage } from '@/components/ChatMessage'
import { loadMe, sendMessage, updateMe } from '@/lib/api'
import type { ChatLine } from '@/lib/types'

// Soft cap on the textarea height: about 6 lines of body text. Anything
// longer scrolls within the textarea instead of pushing the chat list
// off-screen.
const TEXTAREA_MAX_HEIGHT_PX = 160

interface ChatProps {
  history: ChatLine[]
  // channelId is the active channel — user messages route to that channel's
  // orchestrator.
  channelId?: string
}

export function Chat({ history, channelId }: ChatProps) {
  const [draft, setDraft] = useState('')
  // Username comes from the server (GET /api/me sets a cookie on first call).
  // Empty string until the request resolves; chat input stays usable either
  // way because the cookie is what authenticates the message server-side.
  const [username, setUsername] = useState<string>('')
  const [editingName, setEditingName] = useState(false)
  const [nameDraft, setNameDraft] = useState('')
  // sendError surfaces a transient banner when a POST /api/messages fails —
  // without it, a 503 ("no active debate") silently dropped messages and made
  // the chat look broken. Cleared automatically on the next successful send.
  const [sendError, setSendError] = useState<string | null>(null)
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)

  useEffect(() => {
    loadMe()
      .then((me) => {
        setUsername(me.username)
        setNameDraft(me.username)
      })
      .catch(() => {})
  }, [])

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [history.length])

  // Auto-grow the textarea as the user types newlines (and shrink back when
  // the draft is cleared). Cap the height; the textarea's own scrollbar
  // takes over past that.
  useEffect(() => {
    const ta = textareaRef.current
    if (!ta) return
    ta.style.height = 'auto'
    ta.style.height = `${Math.min(ta.scrollHeight, TEXTAREA_MAX_HEIGHT_PX)}px`
  }, [draft])

  const submit = async () => {
    const text = draft.trim()
    if (!text) return
    if (!channelId) {
      setSendError('no channel selected — pick one from the channels panel')
      return
    }
    setDraft('')
    setSendError(null)
    try {
      await sendMessage(text, channelId)
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setSendError(msg)
      // Restore the draft so the user can retry without retyping.
      setDraft(text)
      console.warn('send failed', err)
    }
  }

  const commitName = async () => {
    const trimmed = nameDraft.trim()
    setEditingName(false)
    if (!trimmed || trimmed === username) {
      setNameDraft(username)
      return
    }
    try {
      const me = await updateMe(trimmed)
      setUsername(me.username)
      setNameDraft(me.username)
    } catch {
      // revert on failure
      setNameDraft(username)
    }
  }

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    void submit()
  }

  // Enter sends; Shift+Enter inserts a newline. IME composition (e.g. Chinese
  // input) suppresses the send so the user can still confirm candidates.
  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== 'Enter' || e.shiftKey || e.nativeEvent.isComposing) return
    e.preventDefault()
    void submit()
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
        <div className="flex flex-col leading-tight flex-1 min-w-0">
          <span className="text-sm font-semibold">chat</span>
          <span className="text-[10px] text-muted-foreground">
            {history.length}{' '}
            {history.length === 1 ? 'message' : 'messages'}
          </span>
        </div>
        {editingName ? (
          <input
            autoFocus
            value={nameDraft}
            onChange={(e) => setNameDraft(e.target.value)}
            onBlur={commitName}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitName()
              if (e.key === 'Escape') {
                setNameDraft(username)
                setEditingName(false)
              }
            }}
            maxLength={24}
            className="w-32 rounded-md border border-input bg-input/30 px-2 py-1 text-xs outline-none focus-visible:border-ring focus-visible:ring-[2px] focus-visible:ring-ring/50"
          />
        ) : (
          <button
            type="button"
            onClick={() => {
              setNameDraft(username)
              setEditingName(true)
            }}
            title="click to change your display name"
            className="inline-flex items-center gap-1.5 rounded-full bg-primary/10 hover:bg-primary/20 transition-colors px-2 py-1 text-[11px] font-medium text-primary ring-1 ring-primary/30"
          >
            <User weight="bold" className="h-3 w-3" />
            <span className="max-w-[120px] truncate">{username || '…'}</span>
          </button>
        )}
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
              <ChatMessage key={i} line={line} localUsername={username} />
            ))}
          </ol>
        )}
      </div>

      {sendError && (
        <div className="mx-3 mt-2 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-[11px] text-red-300">
          <div className="flex items-start justify-between gap-2">
            <span className="break-words">{sendError}</span>
            <button
              type="button"
              onClick={() => setSendError(null)}
              className="text-red-300/70 hover:text-red-200 leading-none"
              aria-label="dismiss"
            >
              ×
            </button>
          </div>
        </div>
      )}
      <form
        onSubmit={onSubmit}
        className="flex items-end gap-2 p-3 border-t border-border/40 bg-card/40"
      >
        <textarea
          ref={textareaRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKeyDown}
          maxLength={500}
          rows={1}
          autoComplete="off"
          placeholder="say something… (Shift+Enter for newline · /end to wrap up)"
          className="flex-1 min-h-9 resize-none rounded-2xl border border-input bg-input/30 px-3 py-2 text-base leading-snug outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 md:text-sm"
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
