import { useEffect, useRef, useState } from 'react'
import { FileArrowUp, FilmStrip, Spinner, Download, Archive, Clock } from '@phosphor-icons/react'
import {
  jobArchiveURL,
  jobVideoURL,
  loadJob,
  submitJob,
  type JobInfo,
  type Resolution,
} from '@/lib/job'

type View = 'form' | 'running' | 'done' | 'error'

interface LogLine {
  ts: number
  kind: 'status' | 'phase' | 'transcript' | 'error' | 'info' | 'topic' | 'ended'
  text: string
}

// RenderClock tracks the orchestrator's tick events so the SPA can
// show how many seconds of show-time have been rendered so far. The
// runner's audio pipeline paces the encoder, so this is a close proxy
// for the produced video's current duration.
interface RenderClock {
  elapsedMs: number
  remainingMs: number
}

// peekTopicType reads the `type:` field out of a script.md file's YAML
// frontmatter without pulling in a real YAML parser. Returns "" when
// the file isn't readable or doesn't look like a topic file.
async function peekTopicType(file: File): Promise<string> {
  try {
    const head = await file.slice(0, 4096).text()
    const m = head.match(/^type:\s*([a-z-]+)\s*$/m)
    return m ? m[1].trim() : ''
  } catch {
    return ''
  }
}

export function VideoJobView() {
  const [view, setView] = useState<View>('form')
  const [scriptFile, setScriptFile] = useState<File | null>(null)
  const [priorsFile, setPriorsFile] = useState<File | null>(null)
  const [topicType, setTopicType] = useState<string>('')
  const [softSubs, setSoftSubs] = useState(false)
  const [burnSubs, setBurnSubs] = useState(false)
  const [resolution, setResolution] = useState<Resolution>('720p')
  const [submitErr, setSubmitErr] = useState<string>('')
  const [jobID, setJobID] = useState<string>('')
  const [job, setJob] = useState<JobInfo | null>(null)
  const [log, setLog] = useState<LogLine[]>([])
  const [clock, setClock] = useState<RenderClock>({ elapsedMs: 0, remainingMs: 0 })
  const logRef = useRef<HTMLDivElement>(null)
  const isSeries = topicType === 'series'

  // Auto-scroll the log as it grows.
  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight
    }
  }, [log])

  // Detect topic type as soon as a script file is selected so we can
  // gate the series-only inputs.
  useEffect(() => {
    if (!scriptFile) {
      setTopicType('')
      return
    }
    let cancelled = false
    peekTopicType(scriptFile).then((t) => {
      if (!cancelled) setTopicType(t)
    })
    return () => {
      cancelled = true
    }
  }, [scriptFile])

  // SSE + final-state polling: subscribe once we have a jobID. The
  // event stream piggy-backs on /api/events with a channel filter
  // matching the jobID (the server stamps every published message
  // with the job id).
  useEffect(() => {
    if (!jobID) return
    const append = (line: Omit<LogLine, 'ts'>) =>
      setLog((prev) => [...prev, { ...line, ts: Date.now() }])

    const url = `/api/events?channel=${encodeURIComponent(jobID)}`
    const es = new EventSource(url)
    es.addEventListener('open', () => append({ kind: 'info', text: 'connected' }))
    es.addEventListener('status', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as { text?: string }
      if (m.text) append({ kind: 'status', text: m.text })
    })
    es.addEventListener('phase', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as { phase?: string; label?: string }
      append({ kind: 'phase', text: m.label || m.phase || '' })
    })
    es.addEventListener('transcript', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as { speaker?: string; text?: string; done?: boolean }
      if (m.done && m.text) {
        append({ kind: 'transcript', text: `${m.speaker || ''}: ${m.text}` })
      }
    })
    es.addEventListener('error', (ev) => {
      try {
        const m = JSON.parse((ev as MessageEvent).data) as { text?: string }
        if (m.text) append({ kind: 'error', text: m.text })
      } catch {
        // EventSource fires generic 'error' events on connection drop.
      }
    })
    es.addEventListener('tick', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as {
        elapsed_ms?: number
        remaining_ms?: number
      }
      setClock({
        elapsedMs: m.elapsed_ms ?? 0,
        remainingMs: m.remaining_ms ?? 0,
      })
    })
    es.addEventListener('topic', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as {
        title?: string
        show?: string
        season?: number
        episode?: number
      }
      const head = m.show
        ? `${m.show} · S${m.season ?? 0} E${m.episode ?? 0}`
        : m.title || ''
      if (head) append({ kind: 'topic', text: head })
    })
    es.addEventListener('ended', () => {
      append({ kind: 'ended', text: 'orchestrator ended — finalising mp4…' })
    })
    // The 'ended' SSE event fires when orch.Run finishes — but the job
    // runner still has stitching + zip work to do after that. So poll
    // /api/jobs/<id> on a short interval until the server reports
    // status == 'done' or 'error'.
    const poll = setInterval(async () => {
      const j = await loadJob(jobID)
      if (!j) return
      setJob(j)
      if (j.status === 'done') {
        setView('done')
        clearInterval(poll)
        es.close()
      } else if (j.status === 'error') {
        setView('error')
        clearInterval(poll)
        es.close()
      }
    }, 1500)

    return () => {
      clearInterval(poll)
      es.close()
    }
  }, [jobID])

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitErr('')
    if (!scriptFile) {
      setSubmitErr('Pick a script .md file first.')
      return
    }
    try {
      const id = await submitJob({
        script: scriptFile,
        priors: isSeries ? priorsFile : null,
        softSubs: isSeries && softSubs,
        burnSubs: isSeries && burnSubs,
        resolution,
      })
      setJobID(id)
      setView('running')
    } catch (err) {
      setSubmitErr(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <div className="dark relative flex min-h-screen flex-col bg-background text-foreground font-sans">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 opacity-80"
        style={{
          background:
            'radial-gradient(circle at 12% 0%, oklch(0.795 0.184 86.047 / 0.10), transparent 45%), radial-gradient(circle at 90% 100%, oklch(0.541 0.281 293.009 / 0.12), transparent 50%)',
        }}
      />
      <div className="relative mx-auto flex w-full max-w-3xl flex-col gap-6 p-6">
        <header className="flex items-center gap-3">
          <FilmStrip className="size-7 text-primary" weight="duotone" />
          <div>
            <h1 className="text-2xl font-semibold">debate-bot · video</h1>
            <p className="text-sm text-muted-foreground">
              Upload a script, get a downloadable mp4. Series episodes can chain via the priors zip.
            </p>
          </div>
        </header>

        {view === 'form' && (
          <FormView
            scriptFile={scriptFile}
            setScriptFile={setScriptFile}
            priorsFile={priorsFile}
            setPriorsFile={setPriorsFile}
            isSeries={isSeries}
            topicType={topicType}
            softSubs={softSubs}
            setSoftSubs={setSoftSubs}
            burnSubs={burnSubs}
            setBurnSubs={setBurnSubs}
            resolution={resolution}
            setResolution={setResolution}
            onSubmit={onSubmit}
            submitErr={submitErr}
          />
        )}

        {(view === 'running' || view === 'done' || view === 'error') && (
          <ProgressView
            view={view}
            job={job}
            jobID={jobID}
            log={log}
            logRef={logRef}
            clock={clock}
            onReset={() => {
              setView('form')
              setJobID('')
              setJob(null)
              setLog([])
              setClock({ elapsedMs: 0, remainingMs: 0 })
              setScriptFile(null)
              setPriorsFile(null)
              setSoftSubs(false)
              setBurnSubs(false)
            }}
          />
        )}
      </div>
    </div>
  )
}

interface FormViewProps {
  scriptFile: File | null
  setScriptFile: (f: File | null) => void
  priorsFile: File | null
  setPriorsFile: (f: File | null) => void
  isSeries: boolean
  topicType: string
  softSubs: boolean
  setSoftSubs: (b: boolean) => void
  burnSubs: boolean
  setBurnSubs: (b: boolean) => void
  resolution: Resolution
  setResolution: (r: Resolution) => void
  onSubmit: (e: React.FormEvent) => void
  submitErr: string
}

function FormView(props: FormViewProps) {
  return (
    <form onSubmit={props.onSubmit} className="flex flex-col gap-5 rounded-lg border border-border bg-card/40 p-6">
      <FieldFile
        label="Script (.md)"
        accept=".md,text/markdown"
        file={props.scriptFile}
        onChange={props.setScriptFile}
        required
        hint={
          props.scriptFile
            ? props.topicType
              ? `detected type: ${props.topicType}`
              : 'could not detect type — server will validate on submit'
            : 'topic file with YAML frontmatter (channel, type, etc.)'
        }
      />

      <FieldFile
        label="Priors archive (.zip)"
        accept=".zip,application/zip"
        file={props.priorsFile}
        onChange={props.setPriorsFile}
        disabled={!props.isSeries}
        hint={
          props.isSeries
            ? 'optional — extracted into the persistent series archive before this episode runs'
            : 'series only'
        }
      />

      <label className="flex flex-col gap-1.5">
        <span className="text-sm font-medium">Output resolution</span>
        <select
          value={props.resolution}
          onChange={(e) => props.setResolution(e.target.value as Resolution)}
          className="block w-full rounded-md border border-border bg-background/40 px-3 py-2 text-sm"
        >
          <option value="720p">720p (1280×720) — faster</option>
          <option value="1080p">1080p (1920×1080) — sharper</option>
        </select>
        <span className="text-xs text-muted-foreground">
          ffmpeg upscales the renderer's 1280×720 internal canvas with Lanczos.
        </span>
      </label>

      <fieldset className="flex flex-col gap-2 rounded-md border border-border/60 bg-background/40 p-4">
        <legend className="px-1 text-xs uppercase tracking-wider text-muted-foreground">
          Subtitles (series only)
        </legend>
        <Checkbox
          label="Embed subtitle track (soft, mov_text)"
          checked={props.softSubs}
          onChange={props.setSoftSubs}
          disabled={!props.isSeries}
          hint="toggleable in players that support soft subs (VLC, browser <video>)"
        />
        <Checkbox
          label="Burn subtitles into video (re-encode)"
          checked={props.burnSubs}
          onChange={props.setBurnSubs}
          disabled={!props.isSeries}
          hint="hardcoded — visible in any player including QuickTime, but takes longer"
        />
      </fieldset>

      {props.submitErr && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
          {props.submitErr}
        </div>
      )}

      <button
        type="submit"
        disabled={!props.scriptFile}
        className="inline-flex items-center justify-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <FileArrowUp weight="bold" className="size-4" />
        Generate video
      </button>
    </form>
  )
}

interface ProgressViewProps {
  view: View
  job: JobInfo | null
  jobID: string
  log: LogLine[]
  logRef: React.RefObject<HTMLDivElement | null>
  clock: RenderClock
  onReset: () => void
}

function ProgressView(props: ProgressViewProps) {
  const isDone = props.view === 'done'
  const isError = props.view === 'error'
  const j = props.job
  const stem =
    j?.show && j?.season && j?.episode
      ? `${slug(j.show)}-s${pad2(j.season)}e${pad2(j.episode)}`
      : j?.title
        ? slug(j.title)
        : props.jobID

  return (
    <div className="flex flex-col gap-4 rounded-lg border border-border bg-card/40 p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          {!isDone && !isError && (
            <Spinner className="size-5 animate-spin text-primary" />
          )}
          <div>
            <div className="text-sm uppercase tracking-wider text-muted-foreground">
              job {props.jobID.slice(0, 8)} · {j?.status || 'connecting'}
            </div>
            <div className="text-lg font-medium">
              {j?.title || 'preparing…'}
            </div>
            {j?.show && (
              <div className="text-sm text-muted-foreground">
                {j.show} · S{j.season ?? 0} E{j.episode ?? 0}
              </div>
            )}
          </div>
        </div>
        <div className="flex items-center gap-3">
          {!isError && (
            <div className="flex items-center gap-1.5 rounded-md border border-border/60 bg-background/40 px-3 py-1.5 text-sm tabular-nums">
              <Clock className="size-4 text-muted-foreground" />
              <span title="rendered show-time so far">
                {fmtSeconds(props.clock.elapsedMs)}
              </span>
              {props.clock.remainingMs > 0 && (
                <span className="text-muted-foreground">
                  / {fmtSeconds(props.clock.elapsedMs + props.clock.remainingMs)}
                </span>
              )}
            </div>
          )}
          <button
            type="button"
            onClick={props.onReset}
            className="rounded-md border border-border bg-background/40 px-3 py-1.5 text-sm transition hover:bg-background/80"
          >
            New job
          </button>
        </div>
      </div>

      {isDone && (
        <div className="flex flex-wrap gap-3">
          {j?.has_video && (
            <a
              href={jobVideoURL(props.jobID)}
              download={`${stem}.mp4`}
              className="inline-flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition hover:bg-primary/90"
            >
              <Download weight="bold" className="size-4" />
              Download video
            </a>
          )}
          {j?.has_archive && (
            <a
              href={jobArchiveURL(props.jobID)}
              download={`${stem}-archive.zip`}
              className="inline-flex items-center gap-2 rounded-md border border-border bg-background/60 px-4 py-2 text-sm font-medium transition hover:bg-background"
            >
              <Archive weight="bold" className="size-4" />
              Download archive (priors)
            </a>
          )}
        </div>
      )}

      {isError && j?.error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
          {j.error}
        </div>
      )}

      <div
        ref={props.logRef}
        className="max-h-[28rem] overflow-y-auto rounded-md border border-border/60 bg-background/40 p-3 text-xs font-mono"
      >
        {props.log.length === 0 ? (
          <div className="text-muted-foreground">
            waiting for events…
          </div>
        ) : (
          props.log.map((l, i) => (
            <div key={i} className={logLineClass(l.kind)}>
              <span className="opacity-50">[{l.kind}]</span> {l.text}
            </div>
          ))
        )}
      </div>
    </div>
  )
}

interface FieldFileProps {
  label: string
  accept: string
  file: File | null
  onChange: (f: File | null) => void
  required?: boolean
  disabled?: boolean
  hint?: string
}

function FieldFile(props: FieldFileProps) {
  return (
    <label className={`flex flex-col gap-1.5 ${props.disabled ? 'opacity-50' : ''}`}>
      <span className="text-sm font-medium">
        {props.label}
        {props.required && <span className="text-destructive"> *</span>}
      </span>
      <input
        type="file"
        accept={props.accept}
        disabled={props.disabled}
        required={props.required}
        onChange={(e) => props.onChange(e.target.files?.[0] || null)}
        className="block w-full rounded-md border border-border bg-background/40 px-3 py-2 text-sm file:mr-3 file:rounded file:border-0 file:bg-primary/15 file:px-2 file:py-1 file:text-primary file:cursor-pointer disabled:cursor-not-allowed"
      />
      {props.file && (
        <span className="text-xs text-muted-foreground">
          {props.file.name} ({(props.file.size / 1024).toFixed(1)} KB)
        </span>
      )}
      {props.hint && !props.file && (
        <span className="text-xs text-muted-foreground">{props.hint}</span>
      )}
    </label>
  )
}

interface CheckboxProps {
  label: string
  checked: boolean
  onChange: (b: boolean) => void
  disabled?: boolean
  hint?: string
}

function Checkbox(props: CheckboxProps) {
  return (
    <label className={`flex items-start gap-2 text-sm ${props.disabled ? 'opacity-50' : ''}`}>
      <input
        type="checkbox"
        checked={props.checked}
        disabled={props.disabled}
        onChange={(e) => props.onChange(e.target.checked)}
        className="mt-0.5 size-4 cursor-pointer accent-primary disabled:cursor-not-allowed"
      />
      <span className="flex flex-col">
        <span>{props.label}</span>
        {props.hint && (
          <span className="text-xs text-muted-foreground">{props.hint}</span>
        )}
      </span>
    </label>
  )
}

function logLineClass(kind: LogLine['kind']): string {
  switch (kind) {
    case 'error':
      return 'text-destructive'
    case 'phase':
      return 'text-amber-300'
    case 'status':
      return 'text-muted-foreground'
    case 'transcript':
      return 'text-foreground'
    case 'info':
      return 'text-sky-300'
    case 'topic':
      return 'text-primary'
    case 'ended':
      return 'text-emerald-300'
  }
}

// fmtSeconds renders milliseconds as MM:SS (or HH:MM:SS over an
// hour). Used to show "rendered show-time" on the progress card.
function fmtSeconds(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) ms = 0
  const total = Math.floor(ms / 1000)
  const s = total % 60
  const m = Math.floor(total / 60) % 60
  const h = Math.floor(total / 3600)
  const pad = (n: number) => n.toString().padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

function pad2(n: number): string {
  return n.toString().padStart(2, '0')
}

function slug(s: string): string {
  return s
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '') || 'job'
}
