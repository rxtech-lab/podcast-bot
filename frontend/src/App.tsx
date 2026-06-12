import { useEffect, useState } from 'react'
import { Chat } from '@/components/Chat'
import { AppHeader } from '@/components/AppHeader'
import {
  ChannelSchedule,
  ChannelSwitcher,
  ChannelSwitcherToggle,
} from '@/components/ChannelSwitcher'
import { VideoStage } from '@/components/VideoStage'
import { VideoJobView } from '@/components/VideoJobView'
import { Login } from '@/components/Login'
import { useDebateEvents } from '@/lib/sse'
import { loadConfig, type ServerMode } from '@/lib/config'

const CHANNELS_OPEN_KEY = 'debate-bot:channels-open'

function App() {
  // Server-reported mode. Defaults to "stream" so the SPA renders the
  // existing tuner UI even on a flake response. The fetch is fire-and-
  // forget on mount; no loading screen — the stream UI is the safe
  // baseline.
  const [mode, setMode] = useState<ServerMode>('stream')
  // needsLogin: server requires a password and we don't yet hold a valid
  // cookie. null = config not loaded yet (show nothing to avoid a flash of
  // either the app or the login form before we know which to render).
  const [needsLogin, setNeedsLogin] = useState<boolean | null>(null)

  useEffect(() => {
    loadConfig().then((c) => {
      setMode(c.mode)
      setNeedsLogin(c.authRequired && !c.authed)
    })
  }, [])

  if (needsLogin === null) return null
  if (needsLogin) return <Login onSuccess={() => setNeedsLogin(false)} />

  if (mode === 'video') {
    return <VideoJobView />
  }

  return <StreamView />
}

function StreamView() {
  const [channelsOpen, setChannelsOpen] = useState<boolean>(() => {
    if (typeof window === 'undefined') return true
    const v = window.localStorage.getItem(CHANNELS_OPEN_KEY)
    return v === null ? true : v === '1'
  })

  useEffect(() => {
    window.localStorage.setItem(CHANNELS_OPEN_KEY, channelsOpen ? '1' : '0')
  }, [channelsOpen])

  const { state, selectChannel } = useDebateEvents()
  const {
    history,
    phase,
    phaseLabel,
    elapsedMs,
    remainingMs,
    status,
    channels,
    selectedChannelId,
    selectedDebateIndex,
    selectedDebateTotal,
  } = state

  // Default the tuned channel to the first running (non-off-air) channel as
  // soon as the channel list lands.
  useEffect(() => {
    if (selectedChannelId) return
    const firstLive =
      channels.find((c) => !c.off_air && c.current_debate_id) ??
      channels.find((c) => !c.off_air) ??
      channels[0]
    if (firstLive) selectChannel(firstLive.id)
  }, [channels, selectedChannelId, selectChannel])

  const selectedChannel = channels.find((c) => c.id === selectedChannelId)
  const airingDebate = selectedChannel?.debates.find(
    (d) => d.id === selectedChannel.current_debate_id,
  )

  useEffect(() => {
    const channelTitle = selectedChannel?.title
    const debateTitle = airingDebate?.title
    if (!channelTitle && !debateTitle) {
      document.title = 'debate-bot — live'
      return
    }
    document.title = debateTitle
      ? `${debateTitle} · ${channelTitle ?? ''} — debate-bot`
      : `${channelTitle} — debate-bot`
  }, [selectedChannel, airingDebate])

  const offAir =
    selectedChannel && selectedChannel.off_air
      ? {
          selectedTitle: selectedChannel.title,
          selectedStatus: 'pending' as const,
        }
      : selectedChannel && !airingDebate && selectedChannel.debates.length === 0
        ? {
            selectedTitle: selectedChannel.title,
            selectedStatus: 'pending' as const,
          }
        : undefined

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
          phaseLabel={phaseLabel}
          elapsedMs={elapsedMs}
          remainingMs={remainingMs}
          status={status}
          currentTopicIndex={selectedDebateIndex}
          totalTopics={selectedDebateTotal}
        />
        <main className="flex-1 flex flex-col md:flex-row min-h-0 gap-3 p-3">
          {channelsOpen ? (
            <ChannelSwitcher
              channels={channels}
              currentChannelId={selectedChannelId}
              onSelect={selectChannel}
              onCollapse={() => setChannelsOpen(false)}
            />
          ) : (
            <ChannelSwitcherToggle onExpand={() => setChannelsOpen(true)} />
          )}
          <div className="flex-1 flex flex-col gap-3 min-w-0 min-h-0">
            <ChannelSchedule channel={selectedChannel} />
            <VideoStage
              phase={phase}
              channelId={selectedChannelId ?? undefined}
              offAir={offAir}
            />
          </div>
          <Chat
            history={history}
            channelId={selectedChannelId ?? undefined}
          />
        </main>
      </div>
    </div>
  )
}

export default App
