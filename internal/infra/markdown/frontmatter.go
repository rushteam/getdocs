package markdown

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// splitFrontMatter 拆出文档开头的 YAML front matter（--- 包裹）。解析失败时视为正文。
func splitFrontMatter(content []byte) (map[string]any, []byte) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	if !bytes.HasPrefix(content, []byte("---")) {
		return map[string]any{}, content
	}
	first := bytes.IndexByte(content, '\n')
	if first < 0 || len(bytes.TrimSpace(content[3:first])) != 0 {
		return map[string]any{}, content
	}
	rest := content[first+1:]
	for off := 0; off < len(rest); {
		end := bytes.IndexByte(rest[off:], '\n')
		line := rest[off:]
		next := len(rest)
		if end >= 0 {
			line = rest[off : off+end]
			next = off + end + 1
		}
		if t := bytes.TrimRight(line, " \t\r"); bytes.Equal(t, []byte("---")) || bytes.Equal(t, []byte("...")) {
			meta := map[string]any{}
			if err := yaml.Unmarshal(rest[:off], &meta); err != nil || meta == nil {
				return map[string]any{}, content
			}
			return meta, rest[next:]
		}
		off = next
	}
	return map[string]any{}, content
}
