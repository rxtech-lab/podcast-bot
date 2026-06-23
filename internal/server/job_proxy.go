package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// jobRouteTarget inspects a request and returns the job id it concerns plus a
// sub-resource selector, when the request is one the cross-pod proxy can route.
// ok=false means "not a per-job request" (serve locally).
//
// Two URL shapes carry a job id in jobs mode:
//   - /api/jobs/<id>/<sub>...   -> sub = "<sub>..." (e.g. "ws", "hls/seg.ts")
//   - /api/events?channel=<id>  -> sub = "events"  (live SSE progress stream)
//
// The live event stream is the important second case: both frontends subscribe
// to /api/events?channel=<jobID> for transcript/phase/tick/status, and the
// owner pod's in-memory event bus is the only one that has those events.
func jobRouteTarget(r *http.Request) (id, sub string, ok bool) {
	path := r.URL.Path
	const prefix = "/api/jobs/"
	if strings.HasPrefix(path, prefix) {
		rest := strings.TrimPrefix(path, prefix)
		slash := strings.IndexByte(rest, '/')
		if slash <= 0 || slash >= len(rest)-1 {
			// Bare collection (/api/jobs), submit, or detail (/api/jobs/<id>):
			// served from shared state on any pod.
			return "", "", false
		}
		return rest[:slash], rest[slash+1:], true
	}
	if path == "/api/events" {
		if ch := r.URL.Query().Get("channel"); ch != "" {
			return ch, "events", true
		}
	}
	return "", "", false
}

// ownerLocalWhenDone reports whether a finished-job sub-resource is still only
// available on the owner pod's local filesystem, so requests for it must keep
// being proxied to the owner even after the job is marked done.
//
// Today the runner uploads the final mp3/mp4 and the subtitles.vtt sidecar to
// object storage; the series archive, the transcript db, and HLS segments stay
// on owner-local disk. The mp3/mp4/vtt are owner-local too when their S3 upload
// was disabled or failed (S3Key/AudioS3Key/SubtitlesS3Key empty), in which case
// we must still proxy.
func ownerLocalWhenDone(sub string, j *Job) bool {
	switch {
	case sub == "video":
		return j.S3Key == "" // shared mp4 in S3 -> any pod can serve it
	case sub == "audio":
		return j.AudioS3Key == "" // shared mp3 in S3 -> any pod can serve it
	case sub == "subtitles" || strings.HasPrefix(sub, "subtitles/"):
		return j.SubtitlesS3Key == "" // shared VTT in S3 -> any pod can serve it
	case sub == "archive",
		sub == "transcript",
		strings.HasPrefix(sub, "hls/"):
		return true
	default:
		// "events" (no events are published after completion), "ws", "messages",
		// "stop": nothing useful to proxy once the job is done.
		return false
	}
}

// withJobProxy reverse-proxies requests for a job to the pod that owns its live
// orchestrator + audio stream. It is a no-op (returns next unchanged) unless
// both PodName and PeerHostFor are configured, so single-pod and local
// deployments are unaffected.
//
// Routing rules:
//   - Only per-job requests are considered (see jobRouteTarget).
//   - A job owned by this pod (or with no recorded owner) is served locally.
//   - While pending/running, every sub-resource is proxied to the owner.
//   - Once done, only owner-local sub-resources (see ownerLocalWhenDone) are
//     proxied; resources with a shared-storage copy are served locally.
func (s *Server) withJobProxy(next http.Handler) http.Handler {
	self := s.d.PodName
	peerFor := s.d.PeerHostFor
	if self == "" || peerFor == nil || s.d.Jobs == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, sub, ok := jobRouteTarget(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		j := s.d.Jobs.Get(id)
		if j == nil || j.OwnerPod == "" || j.OwnerPod == self {
			next.ServeHTTP(w, r)
			return
		}
		live := j.Status == JobPending || j.Status == JobRunning
		if !live && !ownerLocalWhenDone(sub, j) {
			next.ServeHTTP(w, r)
			return
		}
		host := peerFor(j.OwnerPod)
		if host == "" {
			next.ServeHTTP(w, r)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: host})
		// Flush every write so live SSE / chunked MP3 / HLS stream through
		// without the proxy buffering them.
		proxy.FlushInterval = -1
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			s.logger().Warn("job proxy to owner failed",
				"job", id, "owner", j.OwnerPod, "host", host, "sub", sub, "err", err)
			http.Error(w, "owner pod unreachable", http.StatusBadGateway)
		}
		proxy.ServeHTTP(w, r)
	})
}
