import { useEffect, useRef, useState } from 'react'
import Hls from 'hls.js'

type Mode = 'warming' | 'video' | 'audio-fallback'

const HLS_URL = '/api/video/stream.m3u8'
const WARMUP_MS = 60_000
const POLL_MS = 1500

export function VideoStage() {
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const [mode, setMode] = useState<Mode>('warming')
  const [warmingText, setWarmingText] = useState('warming up video stream…')

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
            if (!cancelled) setMode('audio-fallback')
            return
          }
        } catch {
          // network blip; keep retrying
        }
        await new Promise((r) => setTimeout(r, POLL_MS))
      }
      if (!cancelled) {
        setWarmingText(
          "video stream didn't start within the warmup window. check the latest out/<session>/run.log and ffmpeg-encoder.log — most often this means ffmpeg isn't on PATH or the encoder crashed. audio still plays below.",
        )
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

    if (Hls.isSupported()) {
      hls = new Hls({ liveSyncDurationCount: 3, enableWorker: true })
      hls.loadSource(HLS_URL)
      hls.attachMedia(player)
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        if (!cancelled) player.play().catch(() => {})
      })
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (data.fatal) {
          console.warn('hls fatal', data)
          if (!cancelled) setMode('audio-fallback')
        }
      })
    } else if (player.canPlayType('application/vnd.apple.mpegurl')) {
      player.src = HLS_URL
      player.play().catch(() => {})
    } else {
      setMode('audio-fallback')
    }

    return () => {
      cancelled = true
      if (hls) hls.destroy()
    }
  }, [mode])

  return (
    <section className="flex-1 min-h-0 flex items-center justify-center bg-black p-2 sm:p-4 relative">
      {mode === 'video' && (
        <div className="w-full h-full flex flex-col gap-2">
          <video
            ref={videoRef}
            muted
            autoPlay
            playsInline
            className="flex-1 min-h-0 w-full bg-black rounded outline-none"
          />
          {/* HLS is video-only; TTS audio comes from /api/audio/stream. */}
          <audio
            src="/api/audio/stream"
            controls
            autoPlay
            className="w-full"
          />
        </div>
      )}
      {mode === 'warming' && (
        <div className="text-muted-foreground text-center p-4 sm:p-8 max-w-xl text-sm sm:text-base">
          {warmingText}
        </div>
      )}
      {mode === 'audio-fallback' && (
        <div className="flex flex-col items-center gap-4 text-muted-foreground text-center p-4 sm:p-8 max-w-xl text-sm sm:text-base">
          <p>{warmingText}</p>
          <audio
            src="/api/audio/stream"
            controls
            autoPlay
            className="w-full max-w-md"
          />
        </div>
      )}
    </section>
  )
}
