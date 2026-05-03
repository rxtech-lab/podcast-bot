import { useEffect, useState } from 'react'
import { Chat } from '@/components/Chat'
import { AppHeader } from '@/components/AppHeader'
import { VideoStage } from '@/components/VideoStage'
import { loadHistory } from '@/lib/api'
import { useDebateEvents } from '@/lib/sse'
import type { ChatLine } from '@/lib/types'

function App() {
  const [initialHistory, setInitialHistory] = useState<ChatLine[]>([])

  useEffect(() => {
    loadHistory()
      .then((lines) => setInitialHistory(lines))
      .catch((e) => console.warn('history load failed', e))
  }, [])

  const {
    history,
    phase,
    elapsedMs,
    remainingMs,
    status,
    topics,
    currentTopicId,
    currentTopicIndex,
    totalTopics,
  } = useDebateEvents(initialHistory)

  useEffect(() => {
    const current = topics.find((t) => t.id === currentTopicId)
    const title = current?.title ?? topics[0]?.title
    if (!title) {
      document.title = 'debate-bot — live'
      return
    }
    document.title =
      totalTopics > 1
        ? `[${currentTopicIndex + 1}/${totalTopics}] ${title} — debate-bot`
        : `${title} — debate-bot`
  }, [topics, currentTopicId, currentTopicIndex, totalTopics])

  return (
    <div className="dark relative flex flex-col h-screen overflow-hidden bg-background text-foreground font-sans">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 opacity-80"
        style={{
          background:
            'radial-gradient(circle at 12% 0%, oklch(0.795 0.184 86.047 / 0.10), transparent 45%), radial-gradient(circle at 90% 100%, oklch(0.541 0.281 293.009 / 0.12), transparent 50%)',
        }}
      />
      <div className="relative flex h-full flex-col">
        <AppHeader
          phase={phase}
          elapsedMs={elapsedMs}
          remainingMs={remainingMs}
          status={status}
          topics={topics}
          currentTopicId={currentTopicId}
          currentTopicIndex={currentTopicIndex}
          totalTopics={totalTopics}
        />
        <main className="flex-1 flex flex-col md:flex-row min-h-0 gap-3 p-3">
          <VideoStage phase={phase} />
          <Chat history={history} />
        </main>
      </div>
    </div>
  )
}

export default App
