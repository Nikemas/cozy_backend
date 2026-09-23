// Package media wraps MinIO (S3-compatible) object storage for product
// photo upload/serving, per §10 of the technical spec: a dedicated
// "cozy-media" bucket that the backend writes normalized product photos
// into (normalize.go, routes.go) and that browsers and the mobile app read
// from directly via plain public URLs (config.PublicObjectURL).
package media

import (
	"bytes"
	"context"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Nikemas/cozy_backend/internal/config"
)

// Client wraps MinIO SDK clients scoped to the single bucket cozy_backend
// uses for media.
type Client struct {
	sdk    *minio.Client // internal: bucket management + object writes (reaches MinIO directly)
	public *minio.Client // public: signs URLs the browser will GET
	bucket string
}

// NewClient builds a Client from the MinIO settings in cfg. It only
// constructs the SDK clients locally and does not talk to the network —
// call EnsureBucket to verify/create the bucket.
//
// Two SDK clients are built because signing and reaching MinIO can need
// different hosts: sdk talks to cfg.MinIOEndpoint (e.g. a Docker-internal
// "minio:9000" reachable from the backend), while public signs URLs against
// cfg.MinIOPublicEndpoint (e.g. the server's public domain), since those
// URLs are handed to the browser, which cannot resolve the internal host.
// Presigning is a local computation (no network call), so building public
// here is safe even though nothing ever connects to it directly.
func NewClient(cfg *config.Config) (*Client, error) {
	creds := credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, "")

	sdk, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  creds,
		Secure: cfg.MinIOUseSSL,
	})
	if err != nil {
		return nil, err
	}

	public := sdk
	if cfg.MinIOPublicEndpoint != cfg.MinIOEndpoint || cfg.MinIOPublicUseSSL != cfg.MinIOUseSSL {
		public, err = minio.New(cfg.MinIOPublicEndpoint, &minio.Options{
			Creds:  creds,
			Secure: cfg.MinIOPublicUseSSL,
		})
		if err != nil {
			return nil, err
		}
	}

	return &Client{sdk: sdk, public: public, bucket: cfg.MinIOBucket}, nil
}

// EnsureBucket makes sure the configured bucket exists, creating it if it
// doesn't. It is idempotent: calling it repeatedly (e.g. once per server
// instance at startup) is safe, and a "bucket already owned by you"/"bucket
// already exists" response from a concurrent creation elsewhere is treated
// as success rather than an error.
func (c *Client) EnsureBucket(ctx context.Context) error {
	exists, err := c.sdk.BucketExists(ctx, c.bucket)
	if err != nil {
		return err
	}
	if !exists {
		err = c.sdk.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{})
		if err != nil {
			resp := minio.ToErrorResponse(err)
			if resp.Code != "BucketAlreadyOwnedByYou" && resp.Code != "BucketAlreadyExists" {
				return err
			}
		}
	}
	return c.sdk.SetBucketPolicy(ctx, c.bucket, anonymousReadPolicy(c.bucket))
}

// anonymousReadPolicy is the bucket policy that lets anyone GET objects
// (but not list, write or delete) in bucket. Product photos are served to
// browsers and the mobile app as plain, unsigned URLs
// (config.PublicObjectURL), so the bucket must be world-readable; writes
// still go only through the backend's own credentials (Client.PutObject,
// called by POST /admin/api/media/upload). Applied
// on every startup (idempotent) so a bucket created by hand or by an
// older version of the code gets the policy too.
func anonymousReadPolicy(bucket string) string {
	return `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {"AWS": ["*"]},
      "Action": ["s3:GetObject"],
      "Resource": ["arn:aws:s3:::` + bucket + `/*"]
    }
  ]
}`
}

// PutObject uploads data under objectKey with the given content type.
// objectKey should come from newVariantKeys — this method itself does not
// validate or generate it. Objects are written once under a fresh UUID
// prefix and never modified afterwards, so they are marked immutable for
// browser/proxy caches.
func (c *Client) PutObject(ctx context.Context, objectKey string, data []byte, contentType string) error {
	_, err := c.sdk.PutObject(ctx, c.bucket, objectKey, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType:  contentType,
		CacheControl: "public, max-age=31536000, immutable",
	})
	return err
}

// RemoveObject deletes objectKey. Removing a key that doesn't exist is
// not an error (S3 semantics).
func (c *Client) RemoveObject(ctx context.Context, objectKey string) error {
	return c.sdk.RemoveObject(ctx, c.bucket, objectKey, minio.RemoveObjectOptions{})
}

// PresignGet returns a presigned URL for reading/serving objectKey, valid
// for ttl. Signed against the public endpoint since the browser is what
// dials this URL.
func (c *Client) PresignGet(ctx context.Context, objectKey string, ttl time.Duration) (string, error) {
	u, err := c.public.PresignedGetObject(ctx, c.bucket, objectKey, ttl, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
