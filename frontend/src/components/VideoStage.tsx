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

  useEffect(() => {
    let cancelled = false
    let hls: Hls | null = null

    const showFallback = () => {
      if (cancelled) return
      setMode('audio-fallback')
    }

    const attachHLS = () => {
      const player = videoRef.current
      if (!player) return
      if (player.canPlayType('application/vnd.apple.mpegurl')) {
        player.src = HLS_URL
        player.play().catch(() => {})
        return
      }
      if (Hls.isSupported()) {
        hls = new Hls({ liveSyncDurationCount: 3 })
        hls.loadSource(HLS_URL)
        hls.attachMedia(player)
        hls.on(Hls.Events.MANIFEST_PARSED, () => {
          player.play().catch(() => {})
        })
        hls.on(Hls.Events.ERROR, (_e, data) => {
          if (data.fatal) {
            console.warn('hls fatal', data)
            showFallback()
          }
        })
        return
      }
      showFallback()
    }

    const start = async () => {
      const deadline = Date.now() + WARMUP_MS
      while (!cancelled && Date.now() < deadline) {
        try {
          const resp = await fetch(HLS_URL, { method: 'HEAD', cache: 'no-store' })
          if (resp.ok) {
            if (cancelled) return
            setMode('video')
            // Wait one tick so the <video> element is mounted.
            queueMicrotask(attachHLS)
            return
          }
          if (resp.status !== 404) {
            showFallback()
            return
          }
        } catch {
          // network blip; keep retrying
        }
        await new Promise((r) => setTimeout(r, POLL_MS))
      }
      if (!cancelled) {
        setWarmingText(
          "video unavailable — check server stderr for 'video disabled' (likely missing system font; set DEBATE_BOT_FONT). audio still works below.",
        )
        showFallback()
      }
    }

    void start()

    return () => {
      cancelled = true
      if (hls) hls.destroy()
    }
  }, [])

  return (
    <section className="flex-1 flex items-center justify-center bg-black p-4 relative">
      {mode === 'video' && (
        <video
          ref={videoRef}
          controls
          autoPlay
          playsInline
          className="w-full h-full max-h-[calc(100vh-80px)] bg-black rounded outline-none"
        />
      )}
      {mode === 'warming' && (
        <div className="text-muted-foreground text-center p-8 max-w-xl">
          {warmingText}
        </div>
      )}
      {mode === 'audio-fallback' && (
        <div className="flex flex-col items-center gap-4 text-muted-foreground text-center p-8 max-w-xl">
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
