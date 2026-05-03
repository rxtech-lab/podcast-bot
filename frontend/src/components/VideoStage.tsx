import { useEffect, useRef, useState } from 'react'
import Hls from 'hls.js'
import { SpeakerHigh, WarningCircle } from '@phosphor-icons/react'

type Mode = 'warming' | 'video' | 'audio-fallback'

const HLS_URL = '/api/video/stream.m3u8'
const WARMUP_MS = 60_000
const POLL_MS = 1500

interface VideoStageProps {
  phase: string
}

export function VideoStage({ phase }: VideoStageProps) {
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const [mode, setMode] = useState<Mode>('warming')
  const [warmingText, setWarmingText] = useState('warming up the video stream')
  const [degraded, setDegraded] = useState(false)

  // Phase 1: poll the manifest until ffmpeg has produced segments, then flip
  // mode to 'video'. This effect does not touch the <video> element itself —
  // attaching hls.js here is racy (videoRef.current is still null because the
  // <video> isn't mounted until after the next React commit).
  useEffect(() => {
    let cancelled = false
    const start = async () => {
      const deadline = Date.now() + WARMUP_MS
      while (!cancelled && Date.now() < deadline) {
        try {
          const resp = await fetch(HLS_URL, { method: 'HEAD', cache: 'no-store' })
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
  }, [])

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
      hls.loadSource(HLS_URL)
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
      player.src = HLS_URL
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
  }, [mode])

  const isLive = phase !== 'setup' && phase !== 'ended'

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
            src="/api/audio/stream"
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
