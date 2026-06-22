package compress

import (
	"archive/zip"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	gcompress "github.com/creativeyann17/go-delta/pkg/compress"
	gdecompress "github.com/creativeyann17/go-delta/pkg/decompress"
	"github.com/creativeyann17/go-delta/pkg/verify"
	"github.com/google/uuid"

	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/events"
	"github.com/yann/mist-drive/api/internal/logger"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/s3x"
)

func processItem(ctx context.Context, item Item, cfg *config.Config, q *Queue, s3c *s3x.Client, hub *events.Hub, tracker ProcessingTracker, quotaUpdater QuotaUpdater, log *logger.Logger) error {
	ext := strings.ToLower(filepath.Ext(item.Key))
	switch ext {
	case ".zip":
		return processZIP(ctx, item, cfg, q, s3c, hub, tracker, quotaUpdater, log)
	case ".jpg", ".jpeg":
		return processJPEG(ctx, item, cfg, s3c, hub, tracker, quotaUpdater, log)
	default:
		log.Info("[compress] skipped key=%s reason=unsupported_format", item.Key)
		return nil
	}
}

func processZIP(ctx context.Context, item Item, cfg *config.Config, q *Queue, s3c *s3x.Client, hub *events.Hub, tracker ProcessingTracker, quotaUpdater QuotaUpdater, log *logger.Logger) error {
	free := quota.DiskFree(cfg.DataDir)
	needed := item.Size*3 + 1<<30
	if free > 0 && free < needed {
		log.Warn("[compress] requeued key=%s reason=disk_full free=%s needed=%s",
			item.Key, formatBytes(free), formatBytes(needed))
		_ = q.Enqueue(item)
		return nil
	}
	log.Debug("[compress] disk ok key=%s free=%s", item.Key, formatBytes(free))

	if tracker != nil {
		tracker.AddProcessing(item.Bucket, item.Key)
		if hub != nil {
			hub.Publish(item.Bucket, events.Event{Type: events.FilesChanged})
		}
		defer func() {
			tracker.RemoveProcessing(item.Bucket, item.Key)
			if hub != nil {
				hub.Publish(item.Bucket, events.Event{Type: events.FilesChanged})
			}
		}()
	}

	jobID := uuid.NewString()
	tmpDir := filepath.Join(cfg.DataDir, "compress-tmp", jobID)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("mkdir tmp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpZip := filepath.Join(tmpDir, "source.zip")
	extractDir := filepath.Join(tmpDir, "extracted")
	recompressBase := filepath.Join(tmpDir, "result")
	recompressFile := recompressBase + "_01.zip"

	log.Debug("[compress] downloading key=%s", item.Key)
	rc, err := s3c.GetObject(ctx, item.Bucket, item.Key)
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	f, err := os.Create(tmpZip)
	if err != nil {
		rc.Close()
		return fmt.Errorf("create tmp zip: %w", err)
	}
	_, copyErr := io.Copy(f, rc)
	rc.Close()
	f.Close()
	if copyErr != nil {
		return fmt.Errorf("download: %w", copyErr)
	}

	log.Debug("[compress] extracting key=%s", item.Key)
	decResult, err := gdecompress.Decompress(&gdecompress.Options{
		InputPath:  tmpZip,
		OutputPath: extractDir,
		MaxThreads: cfg.CompressThreads,
		Overwrite:  true,
		Quiet:      true,
	}, nil)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if !decResult.Success() {
		for _, e := range decResult.Errors {
			log.Error("[compress] extract error key=%s err=%v", item.Key, e)
		}
		return fmt.Errorf("extract: %d error(s)", len(decResult.Errors))
	}
	decompressedSize := int64(decResult.DecompressedSize)
	log.Debug("[compress] extracted key=%s decompressed=%s", item.Key, formatBytes(decompressedSize))

	free2 := quota.DiskFree(cfg.DataDir)
	needed2 := decompressedSize + item.Size + 1<<30
	if free2 > 0 && free2 < needed2 {
		log.Warn("[compress] requeued key=%s reason=disk_full_post_extract free=%s needed=%s",
			item.Key, formatBytes(free2), formatBytes(needed2))
		_ = q.Enqueue(item)
		return nil
	}

	log.Debug("[compress] recompressing key=%s level=%d", item.Key, cfg.CompressLevel)
	compResult, err := gcompress.Compress(&gcompress.Options{
		InputPath:    extractDir,
		OutputPath:   recompressBase,
		UseZipFormat: true,
		Level:        cfg.CompressLevel,
		MaxThreads:   1,
		Quiet:        true,
	}, nil)
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}
	if !compResult.Success() {
		for _, e := range compResult.Errors {
			log.Error("[compress] recompress error key=%s err=%v", item.Key, e)
		}
		return fmt.Errorf("compress: %d error(s)", len(compResult.Errors))
	}
	log.Debug("[compress] recompressed key=%s ratio=%.1f%%", item.Key, compResult.CompressionRatio())

	log.Debug("[compress] verifying key=%s (go-delta)", item.Key)
	verResult, verErr := verify.Verify(&verify.Options{
		InputPath:  recompressFile,
		VerifyData: true,
		Quiet:      true,
	}, nil)
	if verErr != nil || !verResult.IsValid() {
		log.Error("[compress] skipped key=%s reason=verify_failed err=%v valid=%v",
			item.Key, verErr, verResult != nil && verResult.IsValid())
		return nil
	}
	log.Debug("[compress] go-delta verify ok key=%s files=%d", item.Key, verResult.FileCount)

	log.Debug("[compress] verifying key=%s (archive/zip)", item.Key)
	if err := validateZip(recompressFile); err != nil {
		log.Error("[compress] skipped key=%s reason=zip_invalid err=%v", item.Key, err)
		return nil
	}
	log.Debug("[compress] archive/zip validation ok key=%s", item.Key)

	resultInfo, err := os.Stat(recompressFile)
	if err != nil {
		return fmt.Errorf("stat result: %w", err)
	}
	resultSize := resultInfo.Size()
	if resultSize >= item.Size {
		log.Info("[compress] skipped key=%s reason=not_smaller original=%s recompressed=%s",
			item.Key, formatBytes(item.Size), formatBytes(resultSize))
		return nil
	}

	_, currentETag, statErr := s3c.StatObjectFull(ctx, item.Bucket, item.Key)
	if statErr != nil {
		log.Info("[compress] skipped key=%s reason=object_deleted", item.Key)
		return nil
	}
	if currentETag != item.ETag {
		log.Info("[compress] skipped key=%s reason=etag_changed want=%s got=%s",
			item.Key, item.ETag, currentETag)
		return nil
	}

	log.Info("[compress] replacing key=%s old=%s new=%s", item.Key, formatBytes(item.Size), formatBytes(resultSize))
	rf, err := os.Open(recompressFile)
	if err != nil {
		return fmt.Errorf("open result: %w", err)
	}
	defer rf.Close()
	meta := map[string]string{"mist-source-size": strconv.FormatInt(item.Size, 10)}
	if err := s3c.PutObject(ctx, item.Bucket, item.Key, rf, resultSize, "application/zip", meta); err != nil {
		return fmt.Errorf("put object: %w", err)
	}

	saved := item.Size - resultSize
	ratio := float64(saved) / float64(item.Size) * 100
	log.Info("[compress] done key=%s old=%s new=%s saved=%s ratio=%.1f%%",
		item.Key, formatBytes(item.Size), formatBytes(resultSize), formatBytes(saved), ratio)

	if quotaUpdater != nil {
		if err := quotaUpdater.AddUsedBytes(item.Bucket, -saved); err != nil {
			log.Error("[compress] quota update failed key=%s saved=%s err=%v", item.Key, formatBytes(saved), err)
		}
	}

	if hub != nil {
		hub.Publish(item.Bucket, events.Event{Type: events.FilesChanged})
	}
	return nil
}

func processJPEG(ctx context.Context, item Item, cfg *config.Config, s3c *s3x.Client, hub *events.Hub, tracker ProcessingTracker, quotaUpdater QuotaUpdater, log *logger.Logger) error {
	free := quota.DiskFree(cfg.DataDir)
	needed := item.Size*2 + 256<<20
	if free > 0 && free < needed {
		log.Warn("[compress] requeued key=%s reason=disk_full free=%s needed=%s",
			item.Key, formatBytes(free), formatBytes(needed))
		// JPEGs are not re-enqueued on disk-full to avoid unbounded growth;
		// they'll be picked up again if re-uploaded.
		return nil
	}
	log.Debug("[compress] disk ok key=%s free=%s", item.Key, formatBytes(free))

	if tracker != nil {
		tracker.AddProcessing(item.Bucket, item.Key)
		if hub != nil {
			hub.Publish(item.Bucket, events.Event{Type: events.FilesChanged})
		}
		defer func() {
			tracker.RemoveProcessing(item.Bucket, item.Key)
			if hub != nil {
				hub.Publish(item.Bucket, events.Event{Type: events.FilesChanged})
			}
		}()
	}

	jobID := uuid.NewString()
	tmpDir := filepath.Join(cfg.DataDir, "compress-tmp", jobID)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("mkdir tmp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	srcFile := filepath.Join(tmpDir, "source.jpg")
	dstFile := filepath.Join(tmpDir, "result.jpg")

	log.Debug("[compress] downloading key=%s", item.Key)
	rc, err := s3c.GetObject(ctx, item.Bucket, item.Key)
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	f, err := os.Create(srcFile)
	if err != nil {
		rc.Close()
		return fmt.Errorf("create tmp jpg: %w", err)
	}
	_, copyErr := io.Copy(f, rc)
	rc.Close()
	f.Close()
	if copyErr != nil {
		return fmt.Errorf("download: %w", copyErr)
	}

	log.Debug("[compress] decoding key=%s", item.Key)
	src, err := os.Open(srcFile)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	img, _, err := image.Decode(src)
	src.Close()
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	log.Debug("[compress] re-encoding key=%s quality=%d", item.Key, cfg.CompressJPEGQuality)
	dst, err := os.Create(dstFile)
	if err != nil {
		return fmt.Errorf("create result: %w", err)
	}
	encErr := jpeg.Encode(dst, img, &jpeg.Options{Quality: cfg.CompressJPEGQuality})
	dst.Close()
	if encErr != nil {
		return fmt.Errorf("encode: %w", encErr)
	}

	// Verify the output is a valid JPEG by decoding it
	log.Debug("[compress] verifying key=%s (image/jpeg)", item.Key)
	vf, err := os.Open(dstFile)
	if err != nil {
		return fmt.Errorf("open result for verify: %w", err)
	}
	_, _, verErr := image.Decode(vf)
	vf.Close()
	if verErr != nil {
		log.Error("[compress] skipped key=%s reason=verify_failed err=%v", item.Key, verErr)
		return nil
	}

	resultInfo, err := os.Stat(dstFile)
	if err != nil {
		return fmt.Errorf("stat result: %w", err)
	}
	resultSize := resultInfo.Size()
	if resultSize >= item.Size {
		log.Info("[compress] skipped key=%s reason=not_smaller original=%s recompressed=%s",
			item.Key, formatBytes(item.Size), formatBytes(resultSize))
		return nil
	}

	_, currentETag, statErr := s3c.StatObjectFull(ctx, item.Bucket, item.Key)
	if statErr != nil {
		log.Info("[compress] skipped key=%s reason=object_deleted", item.Key)
		return nil
	}
	if currentETag != item.ETag {
		log.Info("[compress] skipped key=%s reason=etag_changed want=%s got=%s",
			item.Key, item.ETag, currentETag)
		return nil
	}

	log.Info("[compress] replacing key=%s old=%s new=%s", item.Key, formatBytes(item.Size), formatBytes(resultSize))
	rf, err := os.Open(dstFile)
	if err != nil {
		return fmt.Errorf("open result: %w", err)
	}
	defer rf.Close()
	meta := map[string]string{"mist-source-size": strconv.FormatInt(item.Size, 10)}
	if err := s3c.PutObject(ctx, item.Bucket, item.Key, rf, resultSize, "image/jpeg", meta); err != nil {
		return fmt.Errorf("put object: %w", err)
	}

	saved := item.Size - resultSize
	ratio := float64(saved) / float64(item.Size) * 100
	log.Info("[compress] done key=%s old=%s new=%s saved=%s ratio=%.1f%%",
		item.Key, formatBytes(item.Size), formatBytes(resultSize), formatBytes(saved), ratio)

	if quotaUpdater != nil {
		if err := quotaUpdater.AddUsedBytes(item.Bucket, -saved); err != nil {
			log.Error("[compress] quota update failed key=%s saved=%s err=%v", item.Key, formatBytes(saved), err)
		}
	}

	if hub != nil {
		hub.Publish(item.Bucket, events.Event{Type: events.FilesChanged})
	}
	return nil
}

func validateZip(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	r.Close()
	return nil
}
