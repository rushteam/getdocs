// Package search 提供内置的内存全文检索：以章节为单位建立倒排索引，BM25 打分。
// 它作为 SyncHook 接入同步流程，每次文档树出新快照时重建该树的索引。
//
// 分词同时支持英文单词与中文：拉丁字母按单词切分并小写，CJK 字符按单字与相邻双字切分，
// 无需额外的分词词典。需要语义检索时，可实现 docs.Searcher 替换本实现。
package search

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// Index 实现 docs.Searcher 与 docs.SyncHook。
type Index struct {
	mu    sync.RWMutex
	trees map[docs.TreeKey]*treeIndex
}

var (
	_ docs.Searcher = (*Index)(nil)
	_ docs.SyncHook = (*Index)(nil)
)

// NewIndex 创建空索引。
func NewIndex() *Index { return &Index{trees: map[docs.TreeKey]*treeIndex{}} }

type entry struct {
	doc     *docs.Document
	section docs.Section
	length  int
	title   string // 小写文档标题与章节标题，用于额外加分
}

type posting struct {
	entry int
	tf    int
}

type treeIndex struct {
	key      docs.TreeKey
	entries  []entry
	postings map[string][]posting
	avgLen   float64
}

func (*Index) Name() string { return "search-index" }

// OnSync 以新快照重建该文档树的索引。
func (ix *Index) OnSync(_ context.Context, ev docs.SyncEvent) error {
	if ev.Removed {
		ix.mu.Lock()
		delete(ix.trees, ev.Key)
		ix.mu.Unlock()
		return nil
	}
	t := build(ev.Current)
	ix.mu.Lock()
	ix.trees[ev.Key] = t
	ix.mu.Unlock()
	return nil
}

func build(snap *docs.Snapshot) *treeIndex {
	t := &treeIndex{key: snap.Key, postings: map[string][]posting{}}
	total := 0
	for _, d := range snap.Docs {
		for _, sec := range d.Sections {
			text := sec.Heading + "\n" + sec.Text
			if sec.Heading == "" {
				text = d.Title + "\n" + sec.Text
			}
			terms := Tokenize(text)
			if len(terms) == 0 {
				continue
			}
			id := len(t.entries)
			t.entries = append(t.entries, entry{
				doc: d, section: sec, length: len(terms),
				title: strings.ToLower(d.Title + " " + strings.Join(sec.Path, " ")),
			})
			tf := map[string]int{}
			for _, term := range terms {
				tf[term]++
			}
			for term, n := range tf {
				t.postings[term] = append(t.postings[term], posting{entry: id, tf: n})
			}
			total += len(terms)
		}
	}
	if len(t.entries) > 0 {
		t.avgLen = float64(total) / float64(len(t.entries))
	}
	return t
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// Search 在匹配的文档树中检索，返回按分数排序的章节。
func (ix *Index) Search(_ context.Context, q docs.SearchQuery) ([]docs.SearchHit, error) {
	terms := uniq(Tokenize(q.Text))
	if len(terms) == 0 {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	lowerQuery := strings.ToLower(strings.TrimSpace(q.Text))

	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var hits []docs.SearchHit
	for key, t := range ix.trees {
		if q.Namespace != "" && key.Namespace != q.Namespace || q.Tree != "" && key.Tree != q.Tree {
			continue
		}
		scores := map[int]float64{}
		n := float64(len(t.entries))
		for _, term := range terms {
			plist := t.postings[term]
			if len(plist) == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(len(plist))+0.5)/(float64(len(plist))+0.5))
			for _, p := range plist {
				e := t.entries[p.entry]
				tf := float64(p.tf)
				norm := tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(e.length)/t.avgLen))
				scores[p.entry] += idf * norm
			}
		}
		for id, score := range scores {
			e := t.entries[id]
			if strings.Contains(e.title, lowerQuery) {
				score *= 1.5
			}
			if strings.Contains(strings.ToLower(e.section.Text), lowerQuery) {
				score *= 1.2
			}
			hits = append(hits, docs.SearchHit{
				Key: key, Path: e.doc.Path, Slug: e.doc.Slug, Title: e.doc.Title,
				Heading: e.section.Heading, Anchor: e.section.Anchor,
				Snippet: Snippet(e.section.Text, terms, 160), Score: math.Round(score*1000) / 1000,
			})
		}
	}
	slices.SortFunc(hits, func(a, b docs.SearchHit) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.Path, b.Path)
	})
	return dedupe(hits, limit), nil
}

// dedupe 限制同一文档最多返回 3 个章节，避免一篇长文占满结果。
func dedupe(hits []docs.SearchHit, limit int) []docs.SearchHit {
	perDoc := map[string]int{}
	out := hits[:0]
	for _, h := range hits {
		k := h.Key.String() + "/" + h.Path
		if perDoc[k] >= 3 {
			continue
		}
		perDoc[k]++
		out = append(out, h)
		if len(out) == limit {
			break
		}
	}
	return out
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// Tokenize 把文本切分为检索词项。
func Tokenize(s string) []string {
	var out []string
	var word strings.Builder
	var prevCJK rune
	flushWord := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	for _, r := range s {
		switch {
		case isCJK(r):
			flushWord()
			out = append(out, string(r))
			if prevCJK != 0 {
				out = append(out, string(prevCJK)+string(r))
			}
			prevCJK = r
			continue
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_':
			word.WriteRune(unicode.ToLower(r))
		default:
			flushWord()
		}
		prevCJK = 0
	}
	flushWord()
	return out
}

func uniq(terms []string) []string {
	seen := map[string]bool{}
	out := terms[:0:0]
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Snippet 截取包含首个命中词的上下文片段。
func Snippet(text string, terms []string, width int) string {
	text = strings.Join(strings.Fields(text), " ")
	lower := strings.ToLower(text)
	pos := -1
	for _, t := range terms {
		if utf8.RuneCountInString(t) < 2 && len(terms) > 1 {
			continue // 中文单字过于常见，优先用双字或单词定位
		}
		if i := strings.Index(lower, t); i >= 0 && (pos < 0 || i < pos) {
			pos = i
		}
	}
	if pos < 0 && len(terms) > 0 {
		pos = max(strings.Index(lower, terms[0]), 0)
	}
	runes := []rune(text)
	pos = min(pos, len(text)) // 小写化可能改变字节长度，越界时退化为从头截取
	start := utf8.RuneCountInString(text[:pos]) - width/4
	start = max(start, 0)
	end := min(start+width, len(runes))
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
