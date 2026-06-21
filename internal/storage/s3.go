// Package storage uploads finished videos to an S3-compatible bucket and mints
// presigned download URLs. It is optional: when no bucket is configured the
// Uploader is disabled and the engine keeps serving videos from local disk.
package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Config describes the destination bucket. Bucket == "" disables uploads.
// Endpoint is set for S3-compatible stores (Cloudflare R2, MinIO); leave empty
// for AWS S3. Credentials come from the standard AWS environment.
type Config struct {
	Bucket   string
	Region   string
	Endpoint string
	Prefix   string
}

// Uploader wraps an S3 client plus a presigner. A nil/disabled Uploader is
// safe to use — its methods no-op or report Enabled() == false.
type Uploader struct {
	cfg     Config
	client  *s3.Client
	presign *s3.PresignClient
}

// New builds an Uploader from cfg. When cfg.Bucket is empty it returns a
// disabled Uploader and no error, so callers can wire it unconditionally.
func New(ctx context.Context, cfg Config) (*Uploader, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return &Uploader{cfg: cfg}, nil
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
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
