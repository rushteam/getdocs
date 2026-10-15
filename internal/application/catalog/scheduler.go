package catalog

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// Scheduler 按文档树各自的间隔定时同步。
type Scheduler struct {
	svc *Service
	// SyncOnStart 为 true 时启动后立即同步一次（错开执行，避免同时拉取所有仓库）。
	SyncOnStart bool
}

// NewScheduler 创建调度器。
func NewScheduler(svc *Service, syncOnStart bool) *Scheduler {
	return &Scheduler{svc: svc, SyncOnStart: syncOnStart}
}

// Run 阻塞运行直到 ctx 取消。运行期间登记的文档树会自动加入定时同步，删除的文档树在下一轮退出。
func (s *Scheduler) Run(ctx context.Context) {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		closed bool
	)
	s.svc.subscribe(func(spec *docs.TreeSpec) {
		mu.Lock()
		defer mu.Unlock()
		if !closed {
			// 登记后由调用方触发首次同步，这里只负责后续的定时同步。
			wg.Go(func() { s.schedule(ctx, spec) })
		}
	})
	for i, spec := range s.svc.Specs() {
		if !spec.AutoSync && !s.SyncOnStart {
			continue
		}
		wg.Go(func() { s.loop(ctx, spec, time.Duration(i)*2*time.Second) })
	}
	<-ctx.Done()
	s.svc.subscribe(nil)
	mu.Lock()
	closed = true
	mu.Unlock()
	wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context, spec *docs.TreeSpec, stagger time.Duration) {
	if s.SyncOnStart {
		if !sleep(ctx, stagger) {
			return
		}
		s.sync(ctx, spec, docs.TriggerStartup)
	}
	s.schedule(ctx, spec)
}

func (s *Scheduler) schedule(ctx context.Context, spec *docs.TreeSpec) {
	if !spec.AutoSync || spec.Interval <= 0 {
		return
	}
	log := s.svc.deps.Logger.With("tree", spec.Key)
	log.InfoContext(ctx, "scheduled sync enabled", "interval", spec.Interval)
	for {
		// 加入至多 10% 的随机抖动，避免多个文档树同时打到同一个 git 服务。
		jitter := time.Duration(rand.Int64N(int64(spec.Interval/10) + 1))
		if !sleep(ctx, spec.Interval+jitter) || !s.svc.active(spec) {
			return
		}
		s.sync(ctx, spec, docs.TriggerSchedule)
	}
}

func (s *Scheduler) sync(ctx context.Context, spec *docs.TreeSpec, trigger docs.Trigger) {
	if _, err := s.svc.Sync(ctx, spec.Key, trigger, false); errors.Is(err, docs.ErrSyncInProgress) {
		s.svc.deps.Logger.InfoContext(ctx, "skip scheduled sync, already running", "tree", spec.Key)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
