package docs

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Chunk 是面向 RAG 的文档切片：以章节为边界，超长章节按段落再切分。
type Chunk struct {
	ID       string   `json:"id"` // 由文档树、路径、章节与序号决定，内容变化时保持稳定，便于向量库 upsert
	Key      TreeKey  `json:"key"`
	Path     string   `json:"path"`
	Slug     string   `json:"slug"`
	Title    string   `json:"title"`
	Headings []string `json:"headings,omitempty"`
	Anchor   string   `json:"anchor,omitempty"`
	Index    int      `json:"index"`
	Text     string   `json:"text"`
	Hash     string   `json:"hash"` // 切片文本哈希，下游可据此跳过未变化的切片
	Commit   string   `json:"commit,omitempty"`
}

// DefaultChunkSize 是默认的切片最大字符数（按 rune 计）。
const DefaultChunkSize = 1500

// ChunkDocument 把文档切分为 RAG 切片。每个切片文本带上文档标题与章节路径作为上下文前缀。
func ChunkDocument(snap *Snapshot, d *Document, maxRunes int) []Chunk {
	if maxRunes <= 0 {
		maxRunes = DefaultChunkSize
	}
	var out []Chunk
	for _, s := range d.Sections {
		body := strings.TrimSpace(s.Text)
		if body == "" {
			continue
		}
		prefix := d.Title
		if len(s.Path) > 0 {
			prefix += " > " + strings.Join(s.Path, " > ")
		}
		for i, part := range splitText(body, maxRunes) {
			text := prefix + "\n\n" + part
			out = append(out, Chunk{
				ID:       chunkID(snap.Key, d.Path, s.Anchor, i),
				Key:      snap.Key,
				Path:     d.Path,
				Slug:     d.Slug,
				Title:    d.Title,
				Headings: s.Path,
				Anchor:   s.Anchor,
				Index:    i,
				Text:     text,
				Hash:     shortHash(text),
				Commit:   snap.Revision.Commit,
			})
		}
	}
	return out
}

func chunkID(k TreeKey, p, anchor string, i int) string {
	return shortHash(k.String() + "\x00" + p + "\x00" + anchor + "\x00" + strconv.Itoa(i))
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

// splitText 按段落累积切分，单个段落超长时再按 rune 硬切。
func splitText(s string, max int) []string {
	if utf8.RuneCountInString(s) <= max {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
		curLen = 0
	}
	for _, para := range strings.Split(s, "\n\n") {
		n := utf8.RuneCountInString(para)
		if curLen > 0 && curLen+n+2 > max {
			flush()
		}
		for n > max {
			r := []rune(para)
			cur.WriteString(string(r[:max]))
			flush()
			para = string(r[max:])
			n -= max
		}
		if curLen > 0 {
			cur.WriteString("\n\n")
			curLen += 2
		}
		cur.WriteString(para)
		curLen += n
	}
	flush()
	return out
}

// ChunkDelta 是两个快照之间切片层面的增量：下游向量库据此 upsert 与删除，无需全量重建。
type ChunkDelta struct {
	FromVersion string   `json:"from_version,omitempty"`
	ToVersion   string   `json:"to_version"`
	Upsert      []Chunk  `json:"upsert"`
	Delete      []string `json:"delete"` // 需要删除的切片 ID
}

// DiffChunks 计算 prev → cur 的切片增量；prev 为 nil 时全部切片都需要写入。
// 只有新增或修改的文档会重新切片，文本未变的切片不会出现在 Upsert 中。
func DiffChunks(prev, cur *Snapshot, maxRunes int) ChunkDelta {
	delta := ChunkDelta{ToVersion: cur.Version, Upsert: []Chunk{}, Delete: []string{}}
	if prev != nil {
		delta.FromVersion = prev.Version
	}
	cs := Diff(prev, cur)
	oldChunks := func(p string) map[string]string {
		m := map[string]string{}
		if prev == nil {
			return m
		}
		if d := prev.DocByPath(p); d != nil {
			for _, c := range ChunkDocument(prev, d, maxRunes) {
				m[c.ID] = c.Hash
			}
		}
		return m
	}
	for _, p := range append(append([]string(nil), cs.Added...), cs.Modified...) {
		old := oldChunks(p)
		for _, c := range ChunkDocument(cur, cur.DocByPath(p), maxRunes) {
			if h, ok := old[c.ID]; !ok || h != c.Hash {
				delta.Upsert = append(delta.Upsert, c)
			}
			delete(old, c.ID)
		}
		for id := range old {
			delta.Delete = append(delta.Delete, id)
		}
	}
	for _, p := range cs.Deleted {
		for id := range oldChunks(p) {
			delta.Delete = append(delta.Delete, id)
		}
	}
	slices.Sort(delta.Delete)
	return delta
}
