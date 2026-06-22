package compress

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/events"
	"github.com/yann/mist-drive/api/internal/logger"
	"github.com/yann/mist-drive/api/internal/s3x"
)

func Start(cfg *config.Config, q *Queue, s3c *s3x.Client, hub *events.Hub, tracker ProcessingTracker, quota QuotaUpdater, log *logger.Logger) {
	tmpDir := filepath.Join(cfg.DataDir, "compress-tmp")
	if err := os.RemoveAll(tmpDir); err != nil {
		log.Warn("[compress] cleanup tmp on start: %v", err)
	} else {
		log.Info("[compress] cleaned up %s", tmpDir)
	}

	for i := 0; i < cfg.CompressWorkers; i++ {
		go runWorker(i+1, cfg, q, s3c, hub, tracker, quota, log)
	}
}

func runWorker(id int, cfg *config.Config, q *Queue, s3c *s3x.Client, hub *events.Hub, tracker ProcessingTracker, quota QuotaUpdater, log *logger.Logger) {
	log.Info("[compress] worker-%d started", id)
	tick := func() {
		item, err := q.Dequeue()
		if err != nil {
			log.Error("[compress] worker-%d dequeue: %v", id, err)
			return
		}
		if item == nil {
			return
		}
		log.Info("[compress] worker-%d dequeued key=%s size=%s", id, item.Key, formatBytes(item.Size))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := processItem(ctx, *item, cfg, q, s3c, hub, tracker, quota, log); err != nil {
			log.Error("[compress] worker-%d error key=%s: %v", id, item.Key, err)
		}
	}
	tick()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		tick()
	}
}
