// Job-mode SSE + REST helpers, paired with the server's /api/jobs API.
//
// The video-mode server reuses the existing /api/events SSE stream
// (filtered by ?channel=<jobID>) and the existing event envelopes —
// only the registry surface is new.

export type JobStatus = 'pending' | 'running' | 'done' | 'error'

export interface JobInfo {
  id: string
  status: JobStatus
  title?: string
  type?: string
  show?: string
  season?: number
  episode?: number
  error?: string
  created_at: string
  updated_at: string
  has_video: boolean
  has_archive: boolean
}

export type Resolution = '720p' | '1080p'

// submitJob posts the multipart upload and returns the new job id. The
// caller subscribes to /api/events?channel=<id> to follow progress and
// polls /api/jobs/<id> for the final state.
export async function submitJob(opts: {
  script: File
  priors?: File | null
  softSubs?: boolean
  burnSubs?: boolean
  resolution?: Resolution
}): Promise<string> {
  const fd = new FormData()
  fd.append('script', opts.script)
  if (opts.priors) fd.append('priors', opts.priors)
  fd.append('soft_subs', opts.softSubs ? 'true' : 'false')
  fd.append('burn_subs', opts.burnSubs ? 'true' : 'false')
  if (opts.resolution) fd.append('resolution', opts.resolution)

  const resp = await fetch('/api/jobs', { method: 'POST', body: fd })
  if (!resp.ok) {
    const text = await resp.text().catch(() => '')
    throw new Error(text || `submit failed: ${resp.status}`)
  }
  const body = (await resp.json()) as { id: string }
  return body.id
}

export async function loadJob(id: string): Promise<JobInfo | null> {
  const resp = await fetch(`/api/jobs/${encodeURIComponent(id)}`)
  if (!resp.ok) return null
  return (await resp.json()) as JobInfo
}

export const jobVideoURL = (id: string) =>
  `/api/jobs/${encodeURIComponent(id)}/video`

export const jobArchiveURL = (id: string) =>
  `/api/jobs/${encodeURIComponent(id)}/archive`
