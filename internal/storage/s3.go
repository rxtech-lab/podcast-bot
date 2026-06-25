// Package storage uploads finished media to an S3-compatible bucket and mints
// download URLs. It is optional: when no bucket is configured the Uploader is
// disabled and the engine keeps serving media from local disk.
package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Config describes the destination bucket. Bucket == "" disables uploads.
// Endpoint is set for S3-compatible stores (Cloudflare R2, MinIO); leave empty
// for AWS S3. Credentials come from the standard AWS environment.
type Config struct {
	Bucket          string
	Region          string
	Endpoint        string
	Prefix          string
	DownloadBaseURL string
	AccessKeyID     string
	SecretAccessKey string
}

// Uploader wraps an S3 client plus a presigner. A nil/disabled Uploader is
// safe to use — its methods no-op or report Enabled() == false.
type Uploader struct {
	cfg     Config
	client  *s3.Client
	presign *s3.PresignClient
}

// ObjectInfo is the small subset of object metadata callers need after a
// browser/native client has uploaded directly with a presigned URL.
type ObjectInfo struct {
	ContentLength int64
	ContentType   string
}

// New builds an Uploader from cfg. When cfg.Bucket is empty it returns a
// disabled Uploader and no error, so callers can wire it unconditionally.
func New(ctx context.Context, cfg Config) (*Uploader, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return &Uploader{cfg: cfg}, nil
	}
	cfg.DownloadBaseURL = strings.TrimRight(strings.TrimSpace(cfg.DownloadBaseURL), "/")
	if cfg.DownloadBaseURL != "" {
		if _, err := url.ParseRequestURI(cfg.DownloadBaseURL); err != nil {
			return nil, fmt.Errorf("s3 download base url: %w", err)
		}
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" {
		if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
			return nil, fmt.Errorf("s3 credentials: S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must both be set")
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = &cfg.Endpoint
			// Path-style addressing is the safe default for S3-compatible
			// endpoints (R2/MinIO) that don't support virtual-host buckets.
			o.UsePathStyle = true
		}
	})
	return &Uploader{
		cfg:     cfg,
		client:  client,
		presign: s3.NewPresignClient(client),
	}, nil
}

// Enabled reports whether uploads are configured.
func (u *Uploader) Enabled() bool { return u != nil && u.client != nil }

// Key joins the configured prefix with name to form the object key.
func (u *Uploader) Key(name string) string {
	if u.cfg.Prefix == "" {
		return name
	}
	return strings.TrimRight(u.cfg.Prefix, "/") + "/" + name
}

// Upload streams the file at localPath to the bucket under key. No-op (returns
// the key) when the uploader is disabled.
func (u *Uploader) Upload(ctx context.Context, localPath, key string) error {
	if !u.Enabled() {
		return nil
	}
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	contentType := "application/octet-stream"
	if strings.HasSuffix(strings.ToLower(localPath), ".mp4") {
		contentType = "video/mp4"
	} else if strings.HasSuffix(strings.ToLower(localPath), ".mp3") {
		contentType = "audio/mpeg"
	} else if strings.HasSuffix(strings.ToLower(localPath), ".png") {
		contentType = "image/png"
	} else if strings.HasSuffix(strings.ToLower(localPath), ".jpg") || strings.HasSuffix(strings.ToLower(localPath), ".jpeg") {
		contentType = "image/jpeg"
	}
	uploader := manager.NewUploader(u.client)
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      &u.cfg.Bucket,
		Key:         &key,
		Body:        f,
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("s3 upload: %w", err)
	}
	return nil
}

// UploadBytes stores data under key with the given content type. No-op (returns
// nil) when the uploader is disabled. Use for small, already-in-memory artifacts
// (e.g. a rendered PDF) rather than streaming a file from disk.
func (u *Uploader) UploadBytes(ctx context.Context, key, contentType string, data []byte) error {
	if !u.Enabled() {
		return nil
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &u.cfg.Bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("s3 put: %w", err)
	}
	return nil
}

// Download fetches the full object bytes for key. Returns (nil, nil) when the
// uploader is disabled or key is empty.
func (u *Uploader) Download(ctx context.Context, key string) ([]byte, error) {
	if !u.Enabled() || key == "" {
		return nil, nil
	}
	resp, err := u.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &u.cfg.Bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// PresignPut returns a time-limited URL that accepts a direct PUT upload for
// key. The caller must send the same Content-Type header used for signing.
func (u *Uploader) PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	if !u.Enabled() {
		return "", nil
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	req, err := u.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      &u.cfg.Bucket,
		Key:         &key,
		ContentType: &contentType,
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign put: %w", err)
	}
	return req.URL, nil
}

// PresignGet returns a time-limited download URL for key. Empty string when
// the uploader is disabled.
func (u *Uploader) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if !u.Enabled() {
		return "", nil
	}
	req, err := u.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &u.cfg.Bucket,
		Key:    &key,
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign: %w", err)
	}
	return req.URL, nil
}

// Head returns metadata for a stored object.
func (u *Uploader) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if !u.Enabled() || key == "" {
		return ObjectInfo{}, nil
	}
	resp, err := u.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &u.cfg.Bucket,
		Key:    &key,
	})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("head object: %w", err)
	}
	var out ObjectInfo
	if resp.ContentLength != nil {
		out.ContentLength = *resp.ContentLength
	}
	if resp.ContentType != nil {
		out.ContentType = *resp.ContentType
	}
	return out, nil
}

// DownloadURL returns a public/custom-domain URL when configured; otherwise it
// falls back to a presigned S3 URL. Use S3_DOWNLOAD_BASE_URL for R2 custom
// domains or public S3/CDN front doors.
func (u *Uploader) DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if !u.Enabled() || key == "" {
		return "", nil
	}
	if u.cfg.DownloadBaseURL != "" {
		if parsed, err := url.Parse(u.cfg.DownloadBaseURL); err == nil && (parsed.Path == "" || parsed.Path == "/") {
			return url.JoinPath(u.cfg.DownloadBaseURL, u.cfg.Bucket, key)
		}
		return url.JoinPath(u.cfg.DownloadBaseURL, key)
	}
	return u.PresignGet(ctx, key, ttl)
}
