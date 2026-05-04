import { useEffect, useRef, useState } from 'react'
import Hls from 'hls.js'
import {
  CheckCircle,
  Circle,
  SpeakerHigh,
  Television,
  WarningCircle,
} from '@phosphor-icons/react'
import type { SessionStatus } from '@/lib/types'

type Mode = 'warming' | 'video' | 'audio-fallback'

const WARMUP_MS = 60_000
const POLL_MS = 1500

interface OffAirInfo {
  // The selected (off-air) channel — what the user clicked.
  selectedTitle: string
  selectedStatus: SessionStatus
  // The currently-airing channel (sequential mode only). Helps the user
  // understand why the video isn't showing the channel they tuned to.
  airingTitle?: string
  airingChannelId?: string
  onTuneToAiring?: () => void
}

interface VideoStageProps {
  phase: string
  // channelId is set in parallel mode so /api/video/<id>/stream.m3u8 and the
  // matching audio fallback URL point at this channel's encoder. Empty in
  // sequential mode (single shared stream).
  channelId?: string
  // offAir, when set, replaces the video+audio with a "this debate isn't
  // airing" placeholder. Used in sequential mode when the user has tuned to
  // a channel that isn't the currently-airing one.
  offAir?: OffAirInfo
}

export function VideoStage({ phase, channelId, offAir }: VideoStageProps) {
  const hlsUrl = channelId
    ? `/api/video/${encodeURIComponent(channelId)}/stream.m3u8`
    : '/api/video/stream.m3u8'
  const audioUrl = channelId
    ? `/api/audio/${encodeURIComponent(channelId)}/stream`
    : '/api/audio/stream'
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const [mode, setMode] = useState<Mode>('warming')
  const [warmingText, setWarmingText] = useState('warming up the video stream')
  const [degraded, setDegraded] = useState(false)

  // Reset all stream state synchronously when the channel switches. Without
  // this, the old <video> element stays mounted with the previous channel's
  // HLS attached until the warming poll for the new URL completes — and a
  // pending channel never has a manifest, so it would just keep playing the
  // old stream. Using the React "derived state on prop change" pattern (set
  // state during render) avoids the brief flash an effect would produce.
  const [stagedUrl, setStagedUrl] = useState(hlsUrl)
  if (stagedUrl !== hlsUrl) {
    setStagedUrl(hlsUrl)
    setMode('warming')
    setDegraded(false)
    setWarmingText('warming up the video stream')
  }

  // Phase 1: poll the manifest until ffmpeg has produced segments, then flip
  // mode to 'video'. This effect does not touch the <video> element itself —
  // attaching hls.js here is racy (videoRef.current is still null because the
  // <video> isn't mounted until after the next React commit). Skip entirely
  // when off-air so we don't pollute the network log with 404s.
  useEffect(() => {
    if (offAir) return
    let cancelled = false
    const start = async () => {
      const deadline = Date.now() + WARMUP_MS
      while (!cancelled && Date.now() < deadline) {
        try {
          const resp = await fetch(hlsUrl, { method: 'HEAD', cache: 'no-store' })
          if (resp.ok) {
            if (!cancelled) setMode('video')
            return
          }
          if (resp.status !== 404) {
            if (!cancelled) {
              setDegraded(true)
              setMode('audio-fallback')
            }
            return
          }
        } catch {
          // network blip; keep retrying
        }
        await new Promise((r) => setTimeout(r, POLL_MS))
      }
      if (!cancelled) {
        setWarmingText(
          "video didn't start in time. check the latest out/<session>/run.log and ffmpeg-encoder.log — most often this means ffmpeg isn't on PATH or the encoder crashed. audio still plays below.",
        )
        setDegraded(true)
        setMode('audio-fallback')
      }
    }
    void start()
    return () => {
      cancelled = true
    }
    // Re-warm when the channel changes so the new manifest gets polled.
  }, [hlsUrl, offAir])

  // Phase 2: when the <video> element is mounted (mode==='video'), attach
  // hls.js. We do this in a separate effect so it runs *after* React commits
  // the DOM and videoRef.current is real. Prefer hls.js wherever supported —
  // Chromium's canPlayType returns "maybe" for HLS but cannot actually decode
  // it, which is why setting player.src directly produced a black frame.
  useEffect(() => {
    if (mode !== 'video') return
    const player = videoRef.current
    if (!player) return
    let hls: Hls | null = null
    let cancelled = false

    // Browsers block autoplay-with-audio until there's a user gesture, so the
    // initial play() call frequently rejects on cold load. Register a one-shot
    // global listener that retries play() the first time the user presses any
    // key or clicks anywhere — typing "Enter" in the chat counts.
    const tryPlay = () => player.play().catch(() => {})
    const onFirstGesture = () => {
      tryPlay()
      window.removeEventListener('keydown', onFirstGesture)
      window.removeEventListener('pointerdown', onFirstGesture)
    }
    window.addEventListener('keydown', onFirstGesture)
    window.addEventListener('pointerdown', onFirstGesture)

    if (Hls.isSupported()) {
      hls = new Hls({ liveSyncDurationCount: 3, enableWorker: true })
      hls.loadSource(hlsUrl)
      hls.attachMedia(player)
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        if (!cancelled) tryPlay()
      })
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (data.fatal) {
          console.warn('hls fatal', data)
          if (!cancelled) {
            setDegraded(true)
            setMode('audio-fallback')
          }
        }
      })
    } else if (player.canPlayType('application/vnd.apple.mpegurl')) {
      player.src = hlsUrl
      tryPlay()
    } else {
      setDegraded(true)
      setMode('audio-fallback')
    }

    return () => {
      cancelled = true
      window.removeEventListener('keydown', onFirstGesture)
      window.removeEventListener('pointerdown', onFirstGesture)
      if (hls) hls.destroy()
    }
  }, [mode, hlsUrl])

  const isLive = phase !== 'setup' && phase !== 'ended'

  if (offAir) {
    return <OffAirStage info={offAir} />
  }

  return (
    <section className="flex-1 min-h-0 flex items-center justify-center bg-black/80 rounded-2xl overflow-hidden border border-border/50 shadow-2xl shadow-black/50 relative">
      {mode === 'video' && (
        <video
          ref={videoRef}
          autoPlay
          playsInline
          controls
          className="w-full h-full bg-black outline-none"
        />
      )}

      {mode === 'warming' && (
        <div className="flex flex-col items-center gap-5 p-8 max-w-md text-center">
          <div className="relative h-12 w-12">
            <div className="absolute inset-0 rounded-full border-2 border-primary/15" />
            <div className="absolute inset-0 rounded-full border-2 border-transparent border-t-primary border-r-primary/60 animate-spin" />
          </div>
          <div className="space-y-1.5">
            <p className="text-sm font-medium text-foreground/90">
              {warmingText}
            </p>
            <p className="text-xs text-muted-foreground">
              this can take up to a minute on a cold start.
            </p>
          </div>
        </div>
      )}

      {mode === 'audio-fallback' && (
        <div className="flex flex-col items-center gap-4 p-6 sm:p-8 w-full max-w-md text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-amber-500/15 ring-1 ring-amber-500/30">
            {degraded ? (
              <WarningCircle
                weight="duotone"
                className="h-6 w-6 text-amber-300"
              />
            ) : (
              <SpeakerHigh
                weight="duotone"
                className="h-6 w-6 text-amber-300"
              />
            )}
          </div>
          <p className="text-sm text-muted-foreground leading-relaxed">
            {warmingText}
          </p>
          <audio
            src={audioUrl}
            controls
            autoPlay
            className="w-full"
          />
        </div>
      )}

      {mode === 'video' && isLive && (
        <div className="absolute top-3 left-3 flex items-center gap-1.5 rounded-full bg-black/60 backdrop-blur-md px-2.5 py-1 text-[10px] font-bold tracking-widest uppercase text-red-400 ring-1 ring-red-500/40">
          <span className="relative flex h-1.5 w-1.5">
            <span className="absolute inset-0 rounded-full bg-red-500 animate-ping" />
            <span className="relative h-1.5 w-1.5 rounded-full bg-red-500" />
          </span>
          live
        </div>
      )}
    </section>
  )
}

const statusCopy: Record<SessionStatus, string> = {
  pending: "this debate hasn't started yet",
  running: 'this debate is airing on another channel',
  done: 'this debate has finished',
  error: "this debate didn't run to completion",
}

function OffAirStage({ info }: { info: OffAirInfo }) {
  const Icon =
    info.selectedStatus === 'done'
      ? CheckCircle
      : info.selectedStatus === 'error'
        ? WarningCircle
        : Circle

  return (
    <section className="flex-1 min-h-0 flex items-center justify-center bg-black/80 rounded-2xl overflow-hidden border border-border/50 shadow-2xl shadow-black/50 relative">
      <div className="flex flex-col items-center gap-5 p-8 max-w-md text-center">
        <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-muted/40 ring-1 ring-border/60">
          <Icon weight="duotone" className="h-7 w-7 text-muted-foreground/80" />
        </div>
        <div className="space-y-1.5">
          <p className="text-[10px] font-bold tracking-widest uppercase text-muted-foreground">
            off air
          </p>
          <p className="text-base font-medium text-foreground/90">
            {info.selectedTitle}
          </p>
          <p className="text-xs text-muted-foreground">
            {statusCopy[info.selectedStatus]}
          </p>
        </div>
        {info.airingTitle && info.airingChannelId && (
          <button
            type="button"
            onClick={info.onTuneToAiring}
            className="group inline-flex items-center gap-2 rounded-xl bg-primary/15 hover:bg-primary/25 transition-colors px-3 py-2 ring-1 ring-primary/30"
          >
            <Television
              weight="duotone"
              className="h-4 w-4 text-primary"
            />
            <span className="flex flex-col items-start leading-tight text-left">
              <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
                now airing
              </span>
              <span className="text-xs font-medium text-foreground/90 max-w-[260px] truncate">
                {info.airingTitle}
              </span>
            </span>
          </button>
        )}
      </div>
    </section>
  )
}
