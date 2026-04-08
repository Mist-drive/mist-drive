package uploads

import (
	"context"
	"time"
)

// MultipartAborter is the subset of s3x.Client that GC needs. Defined
// here (rather than importing s3x) so tests can stub it and to keep the
// uploads package free of a hard dep on the concrete S3 client.
type MultipartAborter interface {
	AbortMultipart(ctx context.Context, bucket, key, uploadID string) error
}

// GC walks every persisted upload state and, for those older than ttl,
// aborts the underlying S3 multipart upload and deletes the state file.
// Returns the number of uploads reclaimed. Per-upload errors are
// swallowed — GC is best-effort and runs on a ticker; the next pass
// will retry anything that failed this time.
func GC(ctx context.Context, store *Store, s MultipartAborter, ttl time.Duration) int {
	states, _ := store.WalkAll()
	reclaimed := 0
	for _, st := range states {
		if time.Since(st.CreatedAt) <= ttl {
			continue
		}
		_ = s.AbortMultipart(ctx, st.Bucket, st.Key, st.UploadID)
		_ = store.Delete(st.UserID, st.UploadID)
		reclaimed++
	}
	return reclaimed
}
