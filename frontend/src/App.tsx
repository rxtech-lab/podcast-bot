import { useEffect, useState } from 'react'
import { Chat } from '@/components/Chat'
import { StatusBar } from '@/components/StatusBar'
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

  const { history, phase, elapsedMs, remainingMs, status } =
    useDebateEvents(initialHistory)

  return (
    <div className="dark flex flex-col h-screen bg-background text-foreground font-sans">
      <StatusBar
        phase={phase}
        elapsedMs={elapsedMs}
        remainingMs={remainingMs}
        status={status}
      />
      <main className="flex-1 flex flex-col md:flex-row min-h-0">
        <VideoStage />
        <Chat history={history} />
      </main>
    </div>
  )
}

export default App
