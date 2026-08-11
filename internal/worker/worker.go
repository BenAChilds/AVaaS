package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/benachilds/avaas/internal/auth"
	"github.com/benachilds/avaas/internal/clamav"
	"github.com/benachilds/avaas/internal/config"
	"github.com/benachilds/avaas/internal/download"
	"github.com/benachilds/avaas/internal/model"
	"github.com/benachilds/avaas/internal/notify"
	"github.com/benachilds/avaas/internal/store"
)

type Worker struct {
	cfg        config.Config
	store      *store.Store
	cipher     *auth.URLCipher
	downloader *download.Downloader
	clam       clamav.Client
	publisher  notify.Publisher
	log        *slog.Logger
	id         string
}

func New(cfg config.Config, s *store.Store, cipher *auth.URLCipher, p notify.Publisher, log *slog.Logger) (*Worker, error) {
	d, err := download.New(cfg.SpoolDir, cfg.MaxFileBytes, cfg.DownloadTimeout)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return &Worker{cfg: cfg, store: s, cipher: cipher, downloader: d, clam: clamav.Client{Socket: cfg.ClamdSocket, Timeout: cfg.DownloadTimeout}, publisher: p, log: log, id: fmt.Sprintf("%s-%d", host, os.Getpid())}, nil
}
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for i := 0; i < w.cfg.WorkerConcurrency; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.loop(ctx) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); w.maintenance(ctx) }()
	wg.Wait()
	return ctx.Err()
}
func (w *Worker) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = store.Heartbeat(ctx, w.store, w.id)
		j, err := store.ClaimJob(ctx, w.store, w.id, w.cfg.LeaseDuration)
		if errors.Is(err, store.ErrNotFound) {
			sleep(ctx, time.Second)
			continue
		}
		if err != nil {
			w.log.Error("claim job failed", "error", err)
			sleep(ctx, time.Second)
			continue
		}
		w.process(ctx, j)
	}
}
func (w *Worker) process(parent context.Context, j model.Job) {
	ctx, cancel := context.WithTimeout(parent, w.cfg.DownloadTimeout)
	defer cancel()
	leaseCtx, stopLease := context.WithCancel(ctx)
	defer stopLease()
	go func() {
		period := w.cfg.LeaseDuration / 3
		if period < time.Second {
			period = time.Second
		}
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				_ = store.RenewLease(leaseCtx, w.store, j.ID, w.id, w.cfg.LeaseDuration)
			}
		}
	}()
	raw, err := w.cipher.Decrypt(j.EncryptedURL)
	if err != nil {
		_ = store.CompleteJob(ctx, w.store, j.ID, w.id, model.VerdictError, "", "decrypt_failed", 0)
		return
	}
	r, err := w.downloader.Fetch(ctx, j.ID, raw)
	if err != nil {
		w.fail(ctx, j, err)
		return
	}
	defer os.Remove(r.Path)
	if err = store.SetScanning(ctx, w.store, j.ID, w.id, r.Bytes, r.Duration.Milliseconds(), w.cfg.LeaseDuration); err != nil {
		return
	}
	start := time.Now()
	scan, err := w.clam.Scan(ctx, r.Path)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		w.fail(ctx, j, err)
		return
	}
	verdict := model.VerdictClean
	if scan.Infected {
		verdict = model.VerdictInfected
	}
	if err = store.CompleteJob(ctx, w.store, j.ID, w.id, verdict, scan.Signature, "", elapsed); err != nil {
		w.log.Error("complete job failed", "job_id", j.ID, "error", err)
	}
}
func (w *Worker) fail(ctx context.Context, j model.Job, err error) {
	code := "temporary_failure"
	retry := true
	if errors.Is(err, download.ErrBlocked) {
		code = "blocked_destination"
		retry = false
	} else if errors.Is(err, download.ErrTooLarge) {
		code = "file_too_large"
		retry = false
	} else {
		var he download.HTTPError
		if errors.As(err, &he) {
			code = "remote_http_error"
			retry = he.Temporary()
		}
	}
	if !retry {
		_ = store.CompleteJob(ctx, w.store, j.ID, w.id, model.VerdictError, "", code, 0)
		return
	}
	backoff := time.Duration(1<<min(j.Attempts, 6)) * time.Second
	_ = store.RetryJob(ctx, w.store, j.ID, w.id, code, backoff)
}
func (w *Worker) maintenance(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	clean := time.NewTicker(time.Hour)
	defer clean.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = store.Heartbeat(ctx, w.store, w.id)
			w.publishOne(ctx)
		case <-clean.C:
			_ = store.Cleanup(ctx, w.store, w.cfg.ResultRetention)
			_ = download.Cleanup(w.cfg.SpoolDir, w.cfg.DownloadTimeout+time.Hour)
		}
	}
}
func (w *Worker) publishOne(ctx context.Context) {
	e, err := store.PendingEvent(ctx, w.store)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		return
	}
	if err = w.publisher.Publish(ctx, e); err != nil {
		_ = store.EventFailed(ctx, w.store, e.EventID, err.Error())
		return
	}
	_ = store.EventPublished(ctx, w.store, e.EventID)
}
func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
