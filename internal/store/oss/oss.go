package oss

import (
	"context"
	"fmt"
	"io"
	"time"

	alioss "github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// Config holds all configuration needed to connect to Alibaba Cloud OSS.
type Config struct {
	Endpoint         string
	InternalEndpoint string
	AccessKeyID      string
	AccessKeySecret  string
	Bucket           string
	Prefix           string
	PresignTTL       time.Duration
}

// UploadPart represents a completed part of a multipart upload.
type UploadPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

// Client wraps an OSS bucket handle along with configuration.
// It maintains two OSS clients: one using the internal endpoint for data
// operations and one using the public endpoint for presigned URL generation.
type Client struct {
	bucket       *alioss.Bucket
	presignClient *alioss.Client
	cfg          Config
}

// New creates a new Client. If InternalEndpoint is non-empty it is used for
// data operations; otherwise Endpoint is used. The public Endpoint is always
// used for presigned URL generation.
func New(cfg Config) (*Client, error) {
	dataEndpoint := cfg.Endpoint
	if cfg.InternalEndpoint != "" {
		dataEndpoint = cfg.InternalEndpoint
	}

	dataClient, err := alioss.New(dataEndpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("oss: create data client: %w", err)
	}

	bucket, err := dataClient.Bucket(cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("oss: get bucket handle: %w", err)
	}

	presignClient, err := alioss.New(cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("oss: create presign client: %w", err)
	}

	return &Client{
		bucket:        bucket,
		presignClient: presignClient,
		cfg:           cfg,
	}, nil
}

// FullKey prepends the configured prefix to key. If the prefix is empty the
// key is returned unchanged.
func (c *Client) FullKey(key string) string {
	if c.cfg.Prefix == "" {
		return key
	}
	return c.cfg.Prefix + "/" + key
}

// InitMultipartUpload initiates a multipart upload for the given OSS key and
// returns the upload ID assigned by OSS.
func (c *Client) InitMultipartUpload(ctx context.Context, ossKey string) (uploadID string, err error) {
	result, err := c.bucket.InitiateMultipartUpload(ossKey)
	if err != nil {
		return "", fmt.Errorf("oss: initiate multipart upload %q: %w", ossKey, err)
	}
	return result.UploadID, nil
}

// UploadPart uploads a single part and returns the ETag returned by OSS.
// Data is streamed directly from reader; no intermediate buffering is
// performed.
func (c *Client) UploadPart(ctx context.Context, ossKey, uploadID string, partNumber int, reader io.Reader, size int64) (etag string, err error) {
	imur := alioss.InitiateMultipartUploadResult{
		Bucket:   c.cfg.Bucket,
		Key:      ossKey,
		UploadID: uploadID,
	}
	result, err := c.bucket.UploadPart(imur, reader, size, partNumber)
	if err != nil {
		return "", fmt.Errorf("oss: upload part %d for %q (upload %s): %w", partNumber, ossKey, uploadID, err)
	}
	return result.ETag, nil
}

// CompleteMultipartUpload finalises a multipart upload.
func (c *Client) CompleteMultipartUpload(ctx context.Context, ossKey, uploadID string, parts []UploadPart) error {
	imur := alioss.InitiateMultipartUploadResult{
		Bucket:   c.cfg.Bucket,
		Key:      ossKey,
		UploadID: uploadID,
	}

	ossParts := make([]alioss.UploadPart, len(parts))
	for i, p := range parts {
		ossParts[i] = alioss.UploadPart{
			PartNumber: p.PartNumber,
			ETag:       p.ETag,
		}
	}

	_, err := c.bucket.CompleteMultipartUpload(imur, ossParts)
	if err != nil {
		return fmt.Errorf("oss: complete multipart upload %q (upload %s): %w", ossKey, uploadID, err)
	}
	return nil
}

// AbortMultipartUpload cancels an in-progress multipart upload and frees any
// already-uploaded parts.
func (c *Client) AbortMultipartUpload(ctx context.Context, ossKey, uploadID string) error {
	imur := alioss.InitiateMultipartUploadResult{
		Bucket:   c.cfg.Bucket,
		Key:      ossKey,
		UploadID: uploadID,
	}
	if err := c.bucket.AbortMultipartUpload(imur); err != nil {
		return fmt.Errorf("oss: abort multipart upload %q (upload %s): %w", ossKey, uploadID, err)
	}
	return nil
}

// DeleteObject deletes a single object from OSS.
func (c *Client) DeleteObject(ctx context.Context, ossKey string) error {
	if err := c.bucket.DeleteObject(ossKey); err != nil {
		return fmt.Errorf("oss: delete object %q: %w", ossKey, err)
	}
	return nil
}

// GetPresignedURL generates a time-limited, pre-signed GET URL for ossKey
// using the public endpoint. The TTL is taken from Config.PresignTTL.
func (c *Client) GetPresignedURL(ctx context.Context, ossKey string) (string, error) {
	presignBucket, err := c.presignClient.Bucket(c.cfg.Bucket)
	if err != nil {
		return "", fmt.Errorf("oss: get presign bucket handle: %w", err)
	}

	ttlSeconds := int64(c.cfg.PresignTTL.Seconds())
	url, err := presignBucket.SignURL(ossKey, alioss.HTTPGet, ttlSeconds)
	if err != nil {
		return "", fmt.Errorf("oss: sign URL for %q: %w", ossKey, err)
	}
	return url, nil
}

// GetObject returns a streaming ReadCloser for the given OSS key. The caller
// is responsible for closing the returned reader.
func (c *Client) GetObject(ctx context.Context, ossKey string) (io.ReadCloser, error) {
	rc, err := c.bucket.GetObject(ossKey)
	if err != nil {
		return nil, fmt.Errorf("oss: get object %q: %w", ossKey, err)
	}
	return rc, nil
}

// PutObject uploads data from reader to OSS using a single PUT request. It is
// intended for small files where multipart upload is unnecessary.
func (c *Client) PutObject(ctx context.Context, ossKey string, reader io.Reader, size int64) error {
	if err := c.bucket.PutObject(ossKey, reader); err != nil {
		return fmt.Errorf("oss: put object %q: %w", ossKey, err)
	}
	return nil
}

// --- Key generation helpers -------------------------------------------------
// These return logical key strings (no prefix applied). Pass the result
// through Client.FullKey when building the actual OSS object key.

// SignSourceKey returns the OSS key for an original (unsigned) file that is
// part of a signing request.
//
//	sign/{requestID}/source/{fileID}-{originalName}
func SignSourceKey(requestID, fileID, originalName string) string {
	return fmt.Sprintf("sign/%s/source/%s-%s", requestID, fileID, originalName)
}

// SignSignedKey returns the OSS key for the signed output of a signing request.
//
//	sign/{requestID}/signed/{fileID}-{originalName}
func SignSignedKey(requestID, fileID, originalName string) string {
	return fmt.Sprintf("sign/%s/signed/%s-%s", requestID, fileID, originalName)
}

// ReleaseFileKey returns the OSS key for a file in a release package.
//
//	release/{requestID}/{fileID}-{originalName}
func ReleaseFileKey(requestID, fileID, originalName string) string {
	return fmt.Sprintf("release/%s/%s-%s", requestID, fileID, originalName)
}
