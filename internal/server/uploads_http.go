package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/planner"
)

// maxUploadBytes caps an uploaded reference file. markitdown handles documents,
// not media, so a modest ceiling is plenty and protects the server.
const maxUploadBytes = 25 << 20 // 25 MiB

// uploadPresignTTL is how long the presigned URL handed to markitdown stays
// valid — long enough for a slow PDF/docx conversion, short-lived otherwise.
const uploadPresignTTL = 15 * time.Minute

// uploadPutTTL is the short upload window for a direct client-to-S3 PUT.
const uploadPutTTL = 10 * time.Minute

// uploadResponse is the body returned by POST /api/uploads: the original
// filename, either parsed markdown or a direct image URL, and the content type.
type uploadResponse struct {
	Filename string `json:"filename"`
	Key      string `json:"key,omitempty"`
	Markdown string `json:"markdown,omitempty"`
	URL      string `json:"url,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

// uploadKindPodcastAudio marks an upload as full-length podcast audio for the
// upload-own-audio flow: audio MIME required, and the per-subscription-tier
// size cap applies instead of the small reference-document ceiling.
const uploadKindPodcastAudio = "podcast-audio"

type uploadPresignRequest struct {
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
	Kind     string `json:"kind,omitempty"`
}

type uploadPresignResponse struct {
	Key       string            `json:"key"`
	UploadURL string            `json:"upload_url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
}

type uploadCompleteRequest struct {
	Key      string `json:"key"`
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
	Kind     string `json:"kind,omitempty"`
}

// checkUploadKind validates a kind-specific upload: for podcast audio it
// requires the feature gate, an audio MIME type, and returns the caller's
// per-tier byte cap. The default kind keeps the reference-document ceiling.
// Returns cap and an http error message ("" when allowed).
func (s *Server) checkUploadKind(ctx context.Context, userID, kind, mimeType string) (int64, string, int) {
	switch strings.TrimSpace(kind) {
	case "":
		return maxUploadBytes, "", 0
	case uploadKindPodcastAudio:
		if !s.uploadAudioAllowedForUser(ctx, userID) {
			return 0, "upload own audio is not enabled for your account", http.StatusForbidden
		}
		if !isAudioMIME(mimeType) {
			return 0, "podcast-audio uploads must be an audio file", http.StatusBadRequest
		}
		return s.uploadAudioCapBytes(ctx, userID), "", 0
	default:
		return 0, "unknown upload kind", http.StatusBadRequest
	}
}

// handleUploadPresign mints a short-lived PUT URL so native clients can upload
// directly to object storage instead of relaying bytes through the API server.
func (s *Server) handleUploadPresign(w http.ResponseWriter, r *http.Request) {
	if !s.d.Uploader.Enabled() {
		http.Error(w, "uploads require S3 storage", http.StatusServiceUnavailable)
		return
	}
	var req uploadPresignRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	filename := strings.TrimSpace(filepath.Base(req.Filename))
	if filename == "" || filename == "." {
		http.Error(w, "filename is required", http.StatusBadRequest)
		return
	}
	mimeType := normalizedUploadMIME(req.MIMEType)
	ext := strings.ToLower(filepath.Ext(filename))
	user := s.requestUser(r)
	if _, msg, code := s.checkUploadKind(r.Context(), user.ID, req.Kind, mimeType); msg != "" {
		http.Error(w, msg, code)
		return
	}
	key := s.d.Uploader.Key(uploadKeyName(user.ID, ext))
	uploadURL, err := s.d.Uploader.PresignPut(r.Context(), key, mimeType, uploadPutTTL)
	if err != nil || uploadURL == "" {
		http.Error(w, "presign upload", http.StatusInternalServerError)
		return
	}
	writeJSON(w, uploadPresignResponse{
		Key:       key,
		UploadURL: uploadURL,
		Method:    http.MethodPut,
		Headers:   map[string]string{"Content-Type": mimeType},
	})
}

// handleUploadComplete verifies the uploaded object and returns an attachment
// payload. Images are passed through as image URLs for the model; other files
// are converted to markdown by markitdown.
func (s *Server) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	if !s.d.Uploader.Enabled() {
		http.Error(w, "uploads require S3 storage", http.StatusServiceUnavailable)
		return
	}
	var req uploadCompleteRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	user := s.requestUser(r)
	if !s.ownsUploadKey(user.ID, req.Key) {
		http.Error(w, "invalid upload key", http.StatusBadRequest)
		return
	}
	maxBytes, msg, code := s.checkUploadKind(r.Context(), user.ID, req.Kind, normalizedUploadMIME(req.MIMEType))
	if msg != "" {
		http.Error(w, msg, code)
		return
	}
	info, err := s.d.Uploader.Head(r.Context(), req.Key)
	if err != nil {
		http.Error(w, "inspect upload: "+err.Error(), http.StatusBadGateway)
		return
	}
	if info.ContentLength <= 0 {
		http.Error(w, "uploaded file is empty", http.StatusBadRequest)
		return
	}
	if info.ContentLength > maxBytes {
		http.Error(w, "file too large", http.StatusBadRequest)
		return
	}
	mimeType := normalizedUploadMIME(req.MIMEType)
	if mimeType == "application/octet-stream" && strings.TrimSpace(info.ContentType) != "" {
		mimeType = normalizedUploadMIME(info.ContentType)
	}
	filename := strings.TrimSpace(filepath.Base(req.Filename))
	if filename == "" || filename == "." {
		filename = "upload"
	}
	resp, err := s.finishUploadedObject(r.Context(), req.Key, filename, mimeType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, resp)
}

// handleUpload accepts a multipart file, stores it in S3, hands markitdown a
// presigned URL to fetch + parse when needed, and returns the attachment
// payload. This remains as a compatibility path for older clients; new clients
// should use /api/uploads/presign + /api/uploads/complete.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.d.Uploader.Enabled() {
		http.Error(w, "uploads require S3 storage", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "file too large or malformed upload", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Stage to a temp file so the S3 uploader (which streams from a path) can
	// send it. Preserve the extension so markitdown can detect the type.
	ext := strings.ToLower(filepath.Ext(header.Filename))
	tmp, err := os.CreateTemp("", "upload-*"+ext)
	if err != nil {
		http.Error(w, "stage upload", http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		http.Error(w, "save upload", http.StatusInternalServerError)
		return
	}
	tmp.Close()

	user := s.requestUser(r)
	key := s.d.Uploader.Key(uploadKeyName(user.ID, ext))
	if err := s.d.Uploader.Upload(r.Context(), tmpPath, key); err != nil {
		http.Error(w, "store upload: "+err.Error(), http.StatusBadGateway)
		return
	}
	mimeType := normalizedUploadMIME(header.Header.Get("Content-Type"))
	resp, err := s.finishUploadedObject(r.Context(), key, header.Filename, mimeType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) finishUploadedObject(ctx context.Context, key, filename, mimeType string) (uploadResponse, error) {
	fetchURL, err := s.d.Uploader.PresignGet(ctx, key, uploadPresignTTL)
	if err != nil || fetchURL == "" {
		return uploadResponse{}, errUpload("presign upload")
	}
	if isImageMIME(mimeType) || isAudioMIME(mimeType) {
		// Images are sent to the model directly; audio (voice messages) is only
		// ever replayed by humans — neither needs markitdown text extraction.
		return uploadResponse{Filename: filename, Key: key, URL: fetchURL, MIMEType: mimeType}, nil
	}
	if s.d.Env == nil || strings.TrimSpace(s.d.Env.MarkitdownServerURL) == "" {
		return uploadResponse{}, errUpload("file parsing service not configured")
	}

	markdown, err := planner.ConvertFile(ctx, s.d.Env, fetchURL)
	if err != nil {
		return uploadResponse{}, errUpload("parse file: " + err.Error())
	}
	return uploadResponse{Filename: filename, Key: key, Markdown: markdown, URL: fetchURL, MIMEType: mimeType}, nil
}

type errUpload string

func (e errUpload) Error() string { return string(e) }

func uploadKeyName(userID, ext string) string {
	return "uploads/" + safeKeySegment(userID) + "/" + newJobID() + ext
}

func (s *Server) ownsUploadKey(userID, key string) bool {
	prefix := s.d.Uploader.Key("uploads/" + safeKeySegment(userID) + "/")
	return strings.HasPrefix(key, prefix)
}

// validatedAudioKey returns a voice-message storage key only if it is a real
// upload owned by the authenticated sender; otherwise it returns "". This stops a
// participant from persisting an arbitrary bucket key (or a forged URL) that the
// server would later re-sign and hand out as a playback URL.
func (s *Server) validatedAudioKey(userID, key string) string {
	key = strings.TrimSpace(key)
	if key == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return ""
	}
	if !s.ownsUploadKey(userID, key) {
		return ""
	}
	return key
}

// sanitizedAttachments drops any client-supplied storage key the authenticated
// user does not own, so a forged key can never be re-signed by the server and
// replayed to the model later (mirrors validatedAudioKey).
func (s *Server) sanitizedAttachments(userID string, atts []planner.Attachment) []planner.Attachment {
	for i := range atts {
		key := strings.TrimSpace(atts[i].Key)
		if key == "" {
			continue
		}
		if s.d.Uploader == nil || !s.d.Uploader.Enabled() || !s.ownsUploadKey(userID, key) {
			key = ""
		}
		atts[i].Key = key
	}
	return atts
}

// uploadURLRefresher returns a signer that maps a stored upload key to a fresh
// presigned GET URL, used to replay persisted image attachments to the model
// after the original upload URL has expired.
func (s *Server) uploadURLRefresher(ctx context.Context) func(key string) string {
	return func(key string) string {
		if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
			return ""
		}
		url, err := s.d.Uploader.PresignGet(ctx, key, uploadPresignTTL)
		if err != nil {
			return ""
		}
		return url
	}
}

func normalizedUploadMIME(mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if mimeType == "" {
		return "application/octet-stream"
	}
	return mimeType
}

func isImageMIME(mimeType string) bool {
	return strings.HasPrefix(normalizedUploadMIME(mimeType), "image/")
}

func isAudioMIME(mimeType string) bool {
	return strings.HasPrefix(normalizedUploadMIME(mimeType), "audio/")
}

// safeKeySegment makes an arbitrary id safe for use as one S3 key path segment.
func safeKeySegment(s string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
	if mapped == "" {
		return "anon"
	}
	return mapped
}
