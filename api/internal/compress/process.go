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

// minSavingPct is the minimum percentage improvement required before we replace
// an object with its recompressed version.
const minSavingPct = 5

// stdLumaZigzag is the standard JFIF luminance quantization table in zigzag
// scan order. Used to reverse-engineer the libjpeg quality setting from
// a JPEG file's embedded DQT marker.
var stdLumaZigzag = [64]int{
	16, 11, 12, 14, 12, 10, 16, 14,
	13, 14, 18, 17, 16, 19, 24, 40,
	26, 24, 22, 22, 24, 49, 35, 37,
	29, 40, 58, 51, 61, 60, 57, 51,
	56, 55, 64, 72, 92, 78, 64, 68,
	87, 69, 55, 56, 80, 109, 81, 87,
	95, 98, 103, 104, 103, 62, 77, 113,
	121, 112, 100, 120, 92, 100, 103, 99,
}

// estimateJPEGQuality parses the JFIF DQT markers in the file and returns an
// estimated libjpeg quality (1–100) based on the luminance quantization table.
// Returns 0 and an error if the table cannot be found.
func estimateJPEGQuality(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	hdr := make([]byte, 2)
	if _, err := io.ReadFull(f, hdr); err != nil || hdr[0] != 0xFF || hdr[1] != 0xD8 {
		return 0, fmt.Errorf("not a JPEG")
	}

	buf := make([]byte, 2)
	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			return 0, fmt.Errorf("read marker: %w", err)
		}
		if buf[0] != 0xFF {
			return 0, fmt.Errorf("invalid marker byte")
		}
		marker := buf[1]

		// Markers with no length field
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			if marker == 0xD9 {
				break
			}
			continue
		}

		lenBuf := make([]byte, 2)
		if _, err := io.ReadFull(f, lenBuf); err != nil {
			return 0, fmt.Errorf("read length: %w", err)
		}
		segLen := int(lenBuf[0])<<8 | int(lenBuf[1])
		if segLen < 2 {
			return 0, fmt.Errorf("invalid segment length")
		}
		bodyLen := segLen - 2

		if marker == 0xDA { // SOS — compressed data starts, no more headers
			break
		}

		if marker != 0xDB { // not DQT — skip
			if _, err := f.Seek(int64(bodyLen), io.SeekCurrent); err != nil {
				return 0, fmt.Errorf("seek: %w", err)
			}
			continue
		}

		// DQT segment — may contain multiple tables packed together
		data := make([]byte, bodyLen)
		if _, err := io.ReadFull(f, data); err != nil {
			return 0, fmt.Errorf("read DQT: %w", err)
		}
		off := 0
		for off < len(data) {
			pqtq := data[off]
			precision := (pqtq >> 4) & 0xF // 0=8-bit, 1=16-bit
			tableID := pqtq & 0xF
			off++
			tableBytes := 64
			if precision == 1 {
				tableBytes = 128
			}
			if off+tableBytes > len(data) {
				break
			}
			if tableID == 0 { // luminance table
				table := make([]int, 64)
				if precision == 0 {
					for i := range table {
						table[i] = int(data[off+i])
					}
				} else {
					for i := range table {
						table[i] = int(data[off+i*2])<<8 | int(data[off+i*2+1])
					}
				}
				return estimateQualityFromLumaTable(table), nil
			}
			off += tableBytes
		}
	}
	return 0, fmt.Errorf("no luminance quantization table found")
}

func estimateQualityFromLumaTable(table []int) int {
	var sum, count int
	for i, v := range table {
		std := stdLumaZigzag[i]
		if std == 0 || v == 0 {
			continue
		}
		// scale factor: 100→Q50, 0→Q100, 200→Q1
		sf := v * 100 / std
		var q int
		switch {
		case sf <= 0:
			q = 100
		case sf <= 100:
			q = (200 - sf) / 2
		default:
			q = 5000 / sf
		}
		if q > 100 {
			q = 100
		}
		if q < 1 {
			q = 1
		}
		sum += q
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / count
}

// startTracking marks item as in-progress and returns a teardown func to defer.
func startTracking(tracker ProcessingTracker, hub *events.Hub, item Item) func() {
	if tracker == nil {
		return func() {}
	}
	tracker.AddProcessing(item.Bucket, item.Key)
	if hub != nil {
		hub.Publish(item.Bucket, events.Event{Type: events.FilesChanged})
	}
	return func() {
		tracker.RemoveProcessing(item.Bucket, item.Key)
		if hub != nil {
			hub.Publish(item.Bucket, events.Event{Type: events.FilesChanged})
		}
	}
}

// makeTmpDir creates an isolated job directory under dataDir/compress-tmp and
// returns its path along with a cleanup func to defer.
func makeTmpDir(dataDir string) (string, func(), error) {
	tmpDir := filepath.Join(dataDir, "compress-tmp", uuid.NewString())
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir tmp: %w", err)
	}
	return tmpDir, func() { os.RemoveAll(tmpDir) }, nil
}

// downloadObject streams an S3 object to a local file.
func downloadObject(ctx context.Context, s3c *s3x.Client, item Item, dest string) error {
	rc, err := s3c.GetObject(ctx, item.Bucket, item.Key)
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	f, err := os.Create(dest)
	if err != nil {
		rc.Close()
		return fmt.Errorf("create file: %w", err)
	}
	_, copyErr := io.Copy(f, rc)
	rc.Close()
	f.Close()
	if copyErr != nil {
		return fmt.Errorf("download: %w", copyErr)
	}
	return nil
}

// replaceObject validates the ETag hasn't changed since enqueue, uploads the
// result file, updates quota, and notifies the hub.
func replaceObject(ctx context.Context, item Item, resultPath string, resultSize int64, contentType string, s3c *s3x.Client, quotaUpdater QuotaUpdater, hub *events.Hub, log *logger.Logger) error {
	_, currentETag, err := s3c.StatObjectFull(ctx, item.Bucket, item.Key)
	if err != nil {
		log.Info("[compress] skipped key=%s reason=object_deleted", item.Key)
		return nil
	}
	if currentETag != item.ETag {
		log.Info("[compress] skipped key=%s reason=etag_changed want=%s got=%s",
			item.Key, item.ETag, currentETag)
		return nil
	}

	log.Info("[compress] replacing key=%s old=%s new=%s", item.Key, formatBytes(item.Size), formatBytes(resultSize))
	rf, err := os.Open(resultPath)
	if err != nil {
		return fmt.Errorf("open result: %w", err)
	}
	defer rf.Close()
	meta := map[string]string{"mist-source-size": strconv.FormatInt(item.Size, 10)}
	if err := s3c.PutObject(ctx, item.Bucket, item.Key, rf, resultSize, contentType, meta); err != nil {
		return fmt.Errorf("put object: %w", err)
	}

	saved := item.Size - resultSize
	log.Info("[compress] done key=%s old=%s new=%s saved=%s ratio=%.1f%%",
		item.Key, formatBytes(item.Size), formatBytes(resultSize), formatBytes(saved),
		float64(saved)/float64(item.Size)*100)

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

func processItem(ctx context.Context, item Item, cfg *config.Config, q *Queue, s3c *s3x.Client, hub *events.Hub, tracker ProcessingTracker, quotaUpdater QuotaUpdater, log *logger.Logger) error {
	if hasSource, err := s3c.HasSourceMeta(ctx, item.Bucket, item.Key); err == nil && hasSource {
		log.Info("[compress] skipped key=%s reason=already_compressed", item.Key)
		return nil
	}

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

	defer startTracking(tracker, hub, item)()

	tmpDir, cleanup, err := makeTmpDir(cfg.DataDir)
	if err != nil {
		return err
	}
	defer cleanup()

	tmpZip := filepath.Join(tmpDir, "source.zip")
	extractDir := filepath.Join(tmpDir, "extracted")
	recompressBase := filepath.Join(tmpDir, "result")
	recompressFile := recompressBase + "_01.zip"

	log.Debug("[compress] downloading key=%s", item.Key)
	if err := downloadObject(ctx, s3c, item, tmpZip); err != nil {
		return err
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
	if (item.Size-resultSize)*100 < item.Size*minSavingPct {
		log.Info("[compress] skipped key=%s reason=savings_too_small original=%s recompressed=%s",
			item.Key, formatBytes(item.Size), formatBytes(resultSize))
		return nil
	}

	return replaceObject(ctx, item, recompressFile, resultSize, "application/zip", s3c, quotaUpdater, hub, log)
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

	defer startTracking(tracker, hub, item)()

	tmpDir, cleanup, err := makeTmpDir(cfg.DataDir)
	if err != nil {
		return err
	}
	defer cleanup()

	srcFile := filepath.Join(tmpDir, "source.jpg")
	dstFile := filepath.Join(tmpDir, "result.jpg")

	log.Debug("[compress] downloading key=%s", item.Key)
	if err := downloadObject(ctx, s3c, item, srcFile); err != nil {
		return err
	}

	if estimated, err := estimateJPEGQuality(srcFile); err == nil && estimated <= cfg.CompressJPEGQuality {
		log.Info("[compress] skipped key=%s reason=quality_ok estimated=%d target=%d",
			item.Key, estimated, cfg.CompressJPEGQuality)
		return nil
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
	if (item.Size-resultSize)*100 < item.Size*minSavingPct {
		log.Info("[compress] skipped key=%s reason=savings_too_small original=%s recompressed=%s",
			item.Key, formatBytes(item.Size), formatBytes(resultSize))
		return nil
	}

	return replaceObject(ctx, item, dstFile, resultSize, "image/jpeg", s3c, quotaUpdater, hub, log)
}

func validateZip(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	r.Close()
	return nil
}
