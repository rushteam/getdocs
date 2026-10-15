package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"getdocs/internal/domain/docs"
)

// maxAssetSize 是单个资源文件的收录上限，超过则不随快照保存。
const maxAssetSize = 20 << 20

// Builder 从仓库检出中构建文档快照。
type Builder struct {
	Parser docs.Parser
	Logger *slog.Logger
}

// Build 定位文档根、按过滤规则收集文档、解析内容并收录被引用的资源。
func (b *Builder) Build(ctx context.Context, spec *docs.TreeSpec, co docs.Checkout) (*docs.Snapshot, []docs.Blob, error) {
	files, err := co.Files(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list files: %w", err)
	}
	root, err := resolveRoot(spec, files)
	if err != nil {
		return nil, nil, err
	}

	// 单文件文档树：Root 指向一个文档文件。
	var single string
	if slices.Contains(files, root) && spec.Filter.IsDoc(root) {
		single, root = path.Base(root), path.Dir(root)
	}

	var docPaths []string // 相对仓库根
	prefix := ""
	if root != "." {
		prefix = root + "/"
	}
	for _, f := range files {
		rel, ok := strings.CutPrefix(f, prefix)
		if !ok {
			continue
		}
		if single != "" {
			if rel == single {
				docPaths = append(docPaths, f)
			}
			continue
		}
		if spec.Filter.MatchDoc(rel) {
			docPaths = append(docPaths, f)
		}
	}
	if len(docPaths) == 0 {
		return nil, nil, fmt.Errorf("no documents found under %q", root)
	}
	if err := co.Prepare(ctx, []string{root}); err != nil {
		return nil, nil, fmt.Errorf("prepare checkout: %w", err)
	}

	rev := co.Revision()
	now := time.Now().UTC()
	snap := &docs.Snapshot{
		Key:         spec.Key,
		Version:     docs.NewVersion(now, rev),
		Revision:    rev,
		Root:        root,
		Fingerprint: spec.Fingerprint(),
		BuiltAt:     now,
	}
	var blobs []docs.Blob
	maxSize := spec.Filter.MaxSize()
	for _, f := range docPaths {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		content, err := co.ReadFile(ctx, f)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", f, err)
		}
		rel := strings.TrimPrefix(f, prefix)
		if int64(len(content)) > maxSize {
			b.Logger.WarnContext(ctx, "skip oversized document", "tree", spec.Key, "path", rel, "size", len(content))
			continue
		}
		parsed, err := b.Parser.Parse(rel, content)
		if err != nil {
			b.Logger.WarnContext(ctx, "skip unparsable document", "tree", spec.Key, "path", rel, "err", err)
			continue
		}
		if parsed.Draft {
			continue
		}
		if parsed.Content != nil {
			content = parsed.Content // 非 markdown 源文件保存转换后的 markdown
		}
		hash := hashOf(content)
		snap.Docs = append(snap.Docs, &docs.Document{
			Path:     rel,
			Title:    parsed.Title,
			Hash:     hash,
			Size:     int64(len(content)),
			Weight:   parsed.Weight,
			Meta:     parsed.Meta,
			Headings: parsed.Headings,
			Sections: parsed.Sections,
			Content:  content,
			Links:    parsed.Links,
		})
		blobs = append(blobs, docs.Blob{Hash: hash, Data: content})
	}
	if len(snap.Docs) == 0 {
		return nil, nil, fmt.Errorf("all documents under %q were skipped", root)
	}
	snap.Seal() // 先建立文档索引，资源解析依赖它

	assetBlobs := b.collectAssets(ctx, snap, co, files)
	blobs = append(blobs, assetBlobs...)
	snap.Seal()
	return snap, blobs, nil
}

// collectAssets 收录文档引用的、仓库中存在的资源文件（可以位于文档根之外）。
func (b *Builder) collectAssets(ctx context.Context, snap *docs.Snapshot, co docs.Checkout, files []string) []docs.Blob {
	exists := make(map[string]bool, len(files))
	for _, f := range files {
		exists[f] = true
	}
	seen := map[string]bool{}
	var blobs []docs.Blob
	for _, d := range snap.Docs {
		for _, link := range d.Links {
			repoPath, _, ok := snap.RepoRef(d, link)
			if !ok || seen[repoPath] || !exists[repoPath] || !docs.IsAsset(repoPath) {
				continue
			}
			seen[repoPath] = true
			data, err := co.ReadFile(ctx, repoPath)
			if err != nil {
				b.Logger.WarnContext(ctx, "skip unreadable asset", "tree", snap.Key, "path", repoPath, "err", err)
				continue
			}
			if len(data) > maxAssetSize {
				b.Logger.WarnContext(ctx, "skip oversized asset", "tree", snap.Key, "path", repoPath, "size", len(data))
				continue
			}
			h := hashOf(data)
			snap.Assets = append(snap.Assets, docs.Asset{Path: repoPath, Hash: h, Size: int64(len(data))})
			blobs = append(blobs, docs.Blob{Hash: h, Data: data})
		}
	}
	slices.SortFunc(snap.Assets, func(a, b docs.Asset) int { return strings.Compare(a.Path, b.Path) })
	return blobs
}

// resolveRoot 校验配置的文档根，未配置时自动探测。
func resolveRoot(spec *docs.TreeSpec, files []string) (string, error) {
	if spec.Root == "" {
		return docs.DetectRoot(files, spec.Filter), nil
	}
	root := path.Clean(strings.Trim(spec.Root, "/"))
	if docs.Escapes(root) {
		return "", fmt.Errorf("root %q escapes repository", spec.Root)
	}
	if root == "." {
		return root, nil
	}
	if slices.Contains(files, root) {
		if !spec.Filter.IsDoc(root) {
			return "", fmt.Errorf("root %q is not a markdown file", spec.Root)
		}
		return root, nil
	}
	prefix := root + "/"
	for _, f := range files {
		if strings.HasPrefix(f, prefix) {
			return root, nil
		}
	}
	return "", fmt.Errorf("root %q not found in repository", spec.Root)
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
