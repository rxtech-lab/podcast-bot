import { useEffect, useState } from 'react'
import { Chat } from '@/components/Chat'
import { AppHeader } from '@/components/AppHeader'
import {
  ChannelSwitcher,
  ChannelSwitcherToggle,
} from '@/components/ChannelSwitcher'
import { VideoStage } from '@/components/VideoStage'
import { loadHistory } from '@/lib/api'
import { useDebateEvents } from '@/lib/sse'
import type { ChatLine } from '@/lib/types'

const CHANNELS_OPEN_KEY = 'debate-bot:channels-open'

function App() {
  const [initialHistory, setInitialHistory] = useState<ChatLine[]>([])
  const [channelsOpen, setChannelsOpen] = useState<boolean>(() => {
    if (typeof window === 'undefined') return true
    const v = window.localStorage.getItem(CHANNELS_OPEN_KEY)
    return v === null ? true : v === '1'
  })

  useEffect(() => {
    window.localStorage.setItem(CHANNELS_OPEN_KEY, channelsOpen ? '1' : '0')
  }, [channelsOpen])

  useEffect(() => {
    loadHistory()
      .then((lines) => setInitialHistory(lines))
      .catch((e) => console.warn('history load failed', e))
  }, [])

  const { state, selectChannel } = useDebateEvents(initialHistory)
  const {
    history,
    phase,
    elapsedMs,
    remainingMs,
    status,
    topics,
    mode,
    currentTopicId,
    currentTopicIndex,
    totalTopics,
  } = state

  // In parallel mode every channel is live from t=0, so default the tuned
  // channel to the first one as soon as the topic list lands. In sequential
  // mode the current topic id arrives via the `topic` SSE event instead.
  useEffect(() => {
    if (mode !== 'parallel') return
    if (currentTopicId) return
    const first = topics.find((t) => t.status === 'running') ?? topics[0]
    if (first) selectChannel(first.id)
  }, [mode, topics, currentTopicId, selectChannel])

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

  // Pass the channel id to VideoStage / Chat only in parallel mode — in
  // sequential mode the unprefixed routes serve the single shared stream and
  // route user messages to the current orchestrator.
  const activeChannelId =
    mode === 'parallel' ? currentTopicId ?? undefined : undefined

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
          currentTopicIndex={currentTopicIndex}
          totalTopics={totalTopics}
        />
        <main className="flex-1 flex flex-col md:flex-row min-h-0 gap-3 p-3">
          {channelsOpen ? (
            <ChannelSwitcher
              topics={topics}
              mode={mode}
              currentChannelId={currentTopicId}
              onSelect={selectChannel}
              onCollapse={() => setChannelsOpen(false)}
            />
          ) : (
            <ChannelSwitcherToggle onExpand={() => setChannelsOpen(true)} />
          )}
          <VideoStage phase={phase} channelId={activeChannelId} />
          <Chat history={history} channelId={activeChannelId} />
        </main>
      </div>
    </div>
  )
}

export default App
