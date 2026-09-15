// Package media wraps MinIO (S3-compatible) object storage for product
// photo upload/serving, per §10 of the technical spec: a dedicated
// "cozy-media" bucket, presigned URLs for both the admin-panel upload and
// serving images back out.
package media

import (
	"context"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Nikemas/cozy_backend/internal/config"
)

// Client wraps a MinIO SDK client scoped to the single bucket cozy_backend
// uses for media.
type Client struct {
	sdk    *minio.Client
	bucket string
}

// NewClient builds a Client from the MinIO settings in cfg. It only
// constructs the SDK client locally and does not talk to the network —
// call EnsureBucket to verify/create the bucket.
func NewClient(cfg *config.Config) (*Client, error) {
	sdk, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: cfg.MinIOUseSSL,
	})
	if err != nil {
		return nil, err
	}
	return &Client{sdk: sdk, bucket: cfg.MinIOBucket}, nil
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
	if exists {
		return nil
	}

	err = c.sdk.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{})
	if err == nil {
		return nil
	}

	if resp := minio.ToErrorResponse(err); resp.Code == "BucketAlreadyOwnedByYou" || resp.Code == "BucketAlreadyExists" {
		return nil
	}
	return err
}

// PresignPut returns a presigned URL the caller can PUT the object's bytes
// to directly (no credentials of ours ever reach the client), valid for
// ttl. objectKey should come from newObjectKey — this method itself does
// not validate or generate it.
func (c *Client) PresignPut(ctx context.Context, objectKey string, ttl time.Duration) (string, error) {
	u, err := c.sdk.PresignedPutObject(ctx, c.bucket, objectKey, ttl)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// PresignGet returns a presigned URL for reading/serving objectKey, valid
// for ttl.
func (c *Client) PresignGet(ctx context.Context, objectKey string, ttl time.Duration) (string, error) {
	u, err := c.sdk.PresignedGetObject(ctx, c.bucket, objectKey, ttl, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
