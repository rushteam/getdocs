package catalog

import (
	"context"
	"fmt"
	"slices"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// Namespaces 返回全部命名空间。返回的是副本，文档树增删不影响调用方持有的结果。
func (s *Service) Namespaces() []*docs.Namespace {
	s.specMu.RLock()
	defer s.specMu.RUnlock()
	out := make([]*docs.Namespace, len(s.namespaces))
	for i, ns := range s.namespaces {
		cp := *ns
		cp.Trees = slices.Clone(ns.Trees)
		out[i] = &cp
	}
	return out
}

// Namespace 按名称查找命名空间。
func (s *Service) Namespace(name string) (*docs.Namespace, error) {
	for _, ns := range s.Namespaces() {
		if ns.Name == name {
			return ns, nil
		}
	}
	return nil, fmt.Errorf("namespace %s: %w", name, docs.ErrNotFound)
}

// Specs 按配置顺序返回全部文档树。
func (s *Service) Specs() []*docs.TreeSpec {
	var out []*docs.TreeSpec
	for _, ns := range s.Namespaces() {
		out = append(out, ns.Trees...)
	}
	return out
}

// Spec 返回文档树配置。
func (s *Service) Spec(key docs.TreeKey) (*docs.TreeSpec, error) {
	s.specMu.RLock()
	defer s.specMu.RUnlock()
	if spec, ok := s.specs[key]; ok {
		return spec, nil
	}
	return nil, fmt.Errorf("tree %s: %w", key, docs.ErrNotFound)
}

func (s *Service) snapshot(key docs.TreeKey) *docs.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current[key]
}

// Snapshot 返回文档树的当前快照；尚未同步成功时返回 docs.ErrNotFound。
func (s *Service) Snapshot(key docs.TreeKey) (*docs.Snapshot, error) {
	if _, err := s.Spec(key); err != nil {
		return nil, err
	}
	if snap := s.snapshot(key); snap != nil {
		return snap, nil
	}
	return nil, fmt.Errorf("tree %s has not been synced: %w", key, docs.ErrNotFound)
}

// SnapshotAt 加载指定历史版本；version 为空时返回当前快照。
func (s *Service) SnapshotAt(ctx context.Context, key docs.TreeKey, version string) (*docs.Snapshot, error) {
	if version == "" {
		return s.Snapshot(key)
	}
	if cur := s.snapshot(key); cur != nil && cur.Version == version {
		return cur, nil
	}
	if _, err := s.Spec(key); err != nil {
		return nil, err
	}
	return s.deps.Snapshots.Load(ctx, key, version)
}

// Versions 返回已保存的快照版本（从新到旧）。
func (s *Service) Versions(ctx context.Context, key docs.TreeKey) ([]string, error) {
	if _, err := s.Spec(key); err != nil {
		return nil, err
	}
	return s.deps.Snapshots.Versions(ctx, key)
}

// State 返回文档树的同步状态。
func (s *Service) State(ctx context.Context, key docs.TreeKey) (docs.TreeState, error) {
	return s.deps.States.State(ctx, key)
}

// Runs 返回最近的同步记录（从新到旧）。
func (s *Service) Runs(ctx context.Context, key docs.TreeKey, limit int) ([]docs.SyncRun, error) {
	return s.deps.States.Runs(ctx, key, limit)
}

// Running 返回进行中的同步，没有时返回 nil。
func (s *Service) Running(key docs.TreeKey) *docs.SyncRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r := s.running[key]; r != nil {
		cp := *r
		return &cp
	}
	return nil
}

// Asset 读取快照中的资源文件内容，repoPath 为相对仓库根的路径。
func (s *Service) Asset(ctx context.Context, snap *docs.Snapshot, repoPath string) ([]byte, error) {
	a, ok := snap.AssetByPath(repoPath)
	if !ok {
		return nil, fmt.Errorf("asset %s: %w", repoPath, docs.ErrNotFound)
	}
	return s.deps.Snapshots.ReadBlob(ctx, a.Hash)
}

// TreeStatus 汇总一棵文档树的配置、快照与同步状态，供展示层使用。
type TreeStatus struct {
	Spec     *docs.TreeSpec
	Snapshot *docs.Snapshot // 未同步时为 nil
	State    docs.TreeState
	Running  *docs.SyncRun
}

// Status 返回文档树状态汇总。
func (s *Service) Status(ctx context.Context, key docs.TreeKey) (TreeStatus, error) {
	spec, err := s.Spec(key)
	if err != nil {
		return TreeStatus{}, err
	}
	st, err := s.State(ctx, key)
	if err != nil {
		return TreeStatus{}, err
	}
	return TreeStatus{Spec: spec, Snapshot: s.snapshot(key), State: st, Running: s.Running(key)}, nil
}
