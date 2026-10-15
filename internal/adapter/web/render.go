package web

import (
	"strings"
	"sync"

	"github.com/rushteam/getdocs/internal/domain/docs"
	"github.com/rushteam/getdocs/internal/infra/markdown"
)

// renderCache 缓存渲染结果。快照不可变，版本号 + 路径即可唯一确定渲染输出。
type renderCache struct {
	mu    sync.Mutex
	items map[string]markdown.Rendered
	max   int
}

func newRenderCache(max int) *renderCache {
	return &renderCache{items: map[string]markdown.Rendered{}, max: max}
}

func (c *renderCache) get(k string) (markdown.Rendered, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.items[k]
	return r, ok
}

func (c *renderCache) put(k string, r markdown.Rendered) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max {
		clear(c.items) // 版本切换后旧条目不再命中，整体清空足够简单且有效
	}
	c.items[k] = r
}

// render 渲染文档，站内链接改写为本服务的地址。pinned 表示浏览的是历史版本，链接需保留版本参数。
func (s *Server) render(snap *docs.Snapshot, spec *docs.TreeSpec, d *docs.Document, pinned bool) (markdown.Rendered, error) {
	key := strings.Join([]string{snap.Key.String(), snap.Version, d.Path, boolStr(pinned)}, "\x00")
	if r, ok := s.cache.get(key); ok {
		return r, nil
	}
	query := ""
	if pinned {
		query = "?v=" + snap.Version
	}
	r, err := s.md.Render(d, markdown.RenderOptions{Rewrite: func(dest string) string {
		t := snap.ResolveLink(d, dest)
		switch t.Kind {
		case docs.LinkDoc:
			return withFragment(docURL(snap.Key, t.Doc.Slug)+query, t.Fragment)
		case docs.LinkAsset:
			return assetURL(snap.Key, t.RepoPath, snap.Version)
		case docs.LinkRepoFile:
			if u := spec.Source.FileURL(snap.Revision, t.RepoPath); u != "" {
				return withFragment(u, t.Fragment)
			}
		}
		return dest
	}})
	if err != nil {
		return r, err
	}
	s.cache.put(key, r)
	return r, nil
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
