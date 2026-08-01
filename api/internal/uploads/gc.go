package uploads

import (
	"context"
	"time"

	"github.com/yann/mist-drive/api/internal/s3x"
)

// GC walks every persisted upload state and, for those older than ttl,
// aborts the underlying S3 multipart upload and deletes the state file.
// Returns the number of uploads reclaimed. Per-upload errors are
// swallowed — GC is best-effort and runs on a ticker; the next pass
// will retry anything that failed this time.
func GC(ctx context.Context, store *Store, s *s3x.Client, ttl time.Duration) int {
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
