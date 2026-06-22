package s3x

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client wraps two minio-go clients:
//   - mc:       talks to MinIO over the internal docker network (e.g. minio:9000)
//     used for bucket ops, listing, stat, deletes, etc.
//   - presign:  configured with the PUBLIC host (e.g. localhost:9000 or s3.mist.localhost)
//     used ONLY to generate presigned URLs. It never actually dials;
//     it just computes signatures against the public host so the browser
//     can hit the URL directly. If PUBLIC_S3_HOST is empty, presign == mc.
type Client struct {
	mc      *minio.Client
	presign *minio.Client
}

// region is set explicitly on both clients so minio-go skips the
// `GET /<bucket>?location=` auto-discovery call. That matters for the
// presign client: it must never actually dial its configured host (the
// public one) — we only use it to compute signatures.
const region = "us-east-1"

func New(endpoint, access, secret string, useSSL bool, publicHost string) (*Client, error) {
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, err
	}
	presign := mc
	if publicHost != "" && publicHost != endpoint {
		// presign URLs bind to whatever endpoint the client was created with.
		// For the public-facing URLs we want the host the browser will hit.
		// `Secure` must match how the browser will reach it: https unless it's localhost.
		secure := useSSL || !isLocalHost(publicHost)
		pc, err := minio.New(publicHost, &minio.Options{
			Creds:  credentials.NewStaticV4(access, secret, ""),
			Secure: secure,
			Region: region,
		})
		if err != nil {
			return nil, err
		}
		presign = pc
	}
	return &Client{mc: mc, presign: presign}, nil
}

func isLocalHost(hp string) bool {
	// strip optional port
	if u, err := url.Parse("http://" + hp); err == nil {
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

func (c *Client) EnsureBucket(ctx context.Context, bucket string) error {
	ok, err := c.mc.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if !ok {
		return c.mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
	}
	return nil
}

func (c *Client) RemoveBucket(ctx context.Context, bucket string) error {
	objCh := c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true})
	for obj := range objCh {
		if obj.Err != nil {
			continue
		}
		_ = c.mc.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{})
	}
	return c.mc.RemoveBucket(ctx, bucket)
}

func (c *Client) ListObjects(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
	out := []ObjectInfo{}
	for obj := range c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true, WithMetadata: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		var sourceSize int64
		// MinIO returns user metadata keys in canonical HTTP form with the
		// x-amz-meta- prefix stripped, e.g. "Mist-Source-Size". Try both
		// forms to stay resilient across SDK/server versions.
		if v := obj.UserMetadata["Mist-Source-Size"]; v != "" {
			sourceSize, _ = strconv.ParseInt(v, 10, 64)
		} else if v := obj.UserMetadata["X-Amz-Meta-Mist-Source-Size"]; v != "" {
			sourceSize, _ = strconv.ParseInt(v, 10, 64)
		}
		out = append(out, ObjectInfo{Key: obj.Key, Size: obj.Size, ETag: obj.ETag, LastModified: obj.LastModified, SourceSize: sourceSize})
	}
	return out, nil
}

type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"lastModified"`
	SourceSize   int64     `json:"sourceSize,omitempty"` // original size before server-side recompression
}

func (c *Client) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	u, err := c.presign.PresignedGetObject(ctx, bucket, key, ttl, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (c *Client) RemoveObject(ctx context.Context, bucket, key string) error {
	return c.mc.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}

// RemoveObjects deletes every key in `keys` from `bucket` using MinIO's
// batch delete API. Much faster than a per-key loop for folder deletes
// (one request vs N). Individual per-key errors are collected and
// returned as a joined error; partial success is still committed —
// that matches the "we'll reconcile used bytes afterwards" model.
func (c *Client) RemoveObjects(ctx context.Context, bucket string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	ch := make(chan minio.ObjectInfo, len(keys))
	for _, k := range keys {
		ch <- minio.ObjectInfo{Key: k}
	}
	close(ch)
	errs := []string{}
	for rerr := range c.mc.RemoveObjects(ctx, bucket, ch, minio.RemoveObjectsOptions{}) {
		if rerr.Err != nil {
			errs = append(errs, rerr.ObjectName+": "+rerr.Err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("remove objects: %s", errs[0])
	}
	return nil
}

// GetObjectRange fetches a byte range of an object. Caller must Close.
// Used for text previews and content sniffing to avoid downloading entire files.
func (c *Client) GetObjectRange(ctx context.Context, bucket, key string, start, end int64) (io.ReadCloser, error) {
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(start, end); err != nil {
		return nil, err
	}
	return c.mc.GetObject(ctx, bucket, key, opts)
}

// GetObject returns a streaming reader for the object. Caller must Close.
// Used by the folder-zip handler; presigned GET is still preferred for
// single-file downloads.
func (c *Client) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	return c.mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
}

func (c *Client) PutEmptyObject(ctx context.Context, bucket, key string) error {
	_, err := c.mc.PutObject(ctx, bucket, key, bytes.NewReader([]byte{}), 0, minio.PutObjectOptions{})
	return err
}

func (c *Client) StatObject(ctx context.Context, bucket, key string) (int64, error) {
	info, err := c.mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

// StatObjectFull returns size and ETag. Used by the compress pipeline to detect
// if an object was replaced between enqueue and the replace step.
func (c *Client) StatObjectFull(ctx context.Context, bucket, key string) (int64, string, error) {
	info, err := c.mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return 0, "", err
	}
	return info.Size, info.ETag, nil
}

func (c *Client) PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string, meta map[string]string) error {
	_, err := c.mc.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType, UserMetadata: meta})
	return err
}

func (c *Client) CopyObject(ctx context.Context, bucket, srcKey, dstKey string) error {
	dst := minio.CopyDestOptions{Bucket: bucket, Object: dstKey}
	src := minio.CopySrcOptions{Bucket: bucket, Object: srcKey}
	_, err := c.mc.CopyObject(ctx, dst, src)
	return err
}

// --- Multipart ---

type PartURL struct {
	PartNumber int    `json:"partNumber"`
	URL        string `json:"url"`
}

// InitMultipart creates a multipart upload on the internal client, then
// presigns each part URL against the PUBLIC host so the browser can PUT to it.
func (c *Client) InitMultipart(ctx context.Context, bucket, key string, size, partSize int64, ttl time.Duration) (string, []PartURL, error) {
	core := minio.Core{Client: c.mc}
	uploadID, err := core.NewMultipartUpload(ctx, bucket, key, minio.PutObjectOptions{})
	if err != nil {
		return "", nil, err
	}
	n := (size + partSize - 1) / partSize
	urls := make([]PartURL, 0, n)
	for i := int64(1); i <= n; i++ {
		u, err := c.presignPartURL(ctx, bucket, key, uploadID, int(i), ttl)
		if err != nil {
			return "", nil, err
		}
		urls = append(urls, PartURL{PartNumber: int(i), URL: u})
	}
	return uploadID, urls, nil
}

func (c *Client) presignPartURL(ctx context.Context, bucket, key, uploadID string, partNum int, ttl time.Duration) (string, error) {
	v := url.Values{}
	v.Set("uploadId", uploadID)
	v.Set("partNumber", fmt.Sprintf("%d", partNum))
	u, err := c.presign.Presign(ctx, "PUT", bucket, key, ttl, v)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

type CompletePart struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
}

func (c *Client) CompleteMultipart(ctx context.Context, bucket, key, uploadID string, parts []CompletePart) error {
	core := minio.Core{Client: c.mc}
	cps := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		cps[i] = minio.CompletePart{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	_, err := core.CompleteMultipartUpload(ctx, bucket, key, uploadID, cps, minio.PutObjectOptions{})
	return err
}

func (c *Client) AbortMultipart(ctx context.Context, bucket, key, uploadID string) error {
	core := minio.Core{Client: c.mc}
	return core.AbortMultipartUpload(ctx, bucket, key, uploadID)
}

// ListIncompleteUploads returns the number of in-progress multipart
// uploads in the given bucket. Used by the GC integration tests (and
// useful for ops tooling).
func (c *Client) ListIncompleteUploads(ctx context.Context, bucket string) (int, error) {
	n := 0
	for info := range c.mc.ListIncompleteUploads(ctx, bucket, "", true) {
		if info.Err != nil {
			return n, info.Err
		}
		n++
	}
	return n, nil
}
