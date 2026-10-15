package docs

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MarkdownExts 是被识别为文档的文件扩展名。
var MarkdownExts = []string{".md", ".markdown", ".mdx"}

// ProtoExts 是可选收录的 protobuf 接口定义文件扩展名，开启后转换为接口文档。
var ProtoExts = []string{".proto"}

// AssetExts 是会被收录为文档资源的文件扩展名。
var AssetExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true,
	".ico": true, ".bmp": true, ".avif": true, ".pdf": true, ".mp4": true, ".webm": true,
	".drawio": true, ".txt": true, ".csv": true,
}

// indexNames 是目录首页的文件名（不含扩展名，小写）。
var indexNames = map[string]bool{"readme": true, "index": true, "_index": true}

// IsMarkdown 判断文件是否为 markdown 文档。
func IsMarkdown(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	for _, e := range MarkdownExts {
		if ext == e {
			return true
		}
	}
	return false
}

// IsProto 判断文件是否为 protobuf 接口定义。
func IsProto(p string) bool { return strings.EqualFold(path.Ext(p), ".proto") }

// IsAsset 判断文件是否为可收录的资源。
func IsAsset(p string) bool { return AssetExts[strings.ToLower(path.Ext(p))] }

// IsIndexDoc 判断文档是否为目录首页（README.md / index.md）。
func IsIndexDoc(p string) bool {
	base := path.Base(p)
	return IsMarkdown(base) && indexNames[strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))]
}

// SlugOf 把文档路径转换为 URL slug：去掉扩展名，目录首页取其目录，根首页为空串。
func SlugOf(p string) string {
	if IsIndexDoc(p) {
		dir := path.Dir(p)
		if dir == "." {
			return ""
		}
		return dir
	}
	return strings.TrimSuffix(p, path.Ext(p))
}

// ResolveRel 以 base 所在目录为基准解析相对引用 ref（均为相对同一根的路径）。
// 返回的路径可能以 "../" 开头，表示越出根目录。
func ResolveRel(base, ref string) string {
	if strings.HasPrefix(ref, "/") {
		return path.Clean(strings.TrimPrefix(ref, "/"))
	}
	return path.Clean(path.Join(path.Dir(base), ref))
}

// Escapes 判断经 path.Clean 后的相对路径是否越出根目录。
func Escapes(p string) bool { return p == ".." || strings.HasPrefix(p, "../") }

// HumanizeName 把文件名转换为可读标题：去扩展名与序号前缀，连字符转空格。
func HumanizeName(name string) string {
	name = strings.TrimSuffix(name, path.Ext(name))
	if i := strings.IndexFunc(name, func(r rune) bool { return r < '0' || r > '9' }); i > 0 && i < len(name) && (name[i] == '-' || name[i] == '_' || name[i] == '.') {
		name = name[i+1:]
	}
	name = strings.NewReplacer("-", " ", "_", " ").Replace(name)
	r, size := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return name
	}
	return string(unicode.ToUpper(r)) + name[size:]
}
