package protodoc

import (
	"strings"
)

// cleanComment 规整源码注释：去掉块注释每行开头的 "*"、公共缩进以及首尾空行。
func cleanComment(c string) string {
	lines := strings.Split(strings.ReplaceAll(c, "\r\n", "\n"), "\n")
	star := true
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "*") {
			star = false
			break
		}
	}
	if star {
		for i, l := range lines {
			lines[i] = strings.TrimPrefix(strings.TrimLeft(l, " \t"), "*")
		}
	}
	indent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " \t")); indent < 0 || n < indent {
			indent = n
		}
	}
	for i, l := range lines {
		if len(l) >= indent && indent > 0 {
			lines[i] = l[indent:]
		}
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// block 把注释转换为 markdown 段落。注释按 markdown 书写习惯渲染，但不允许产生标题
// （否则会打乱页面结构与锚点），尖括号按字面展示（注释里常见 List<Item>、<id> 之类的写法）。
func block(c string) string {
	if c == "" {
		return ""
	}
	lines := strings.Split(c, "\n")
	inFence := ""
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if inFence != "" {
			if strings.HasPrefix(t, inFence) {
				inFence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = t[:3]
			continue
		}
		switch {
		case strings.HasPrefix(t, "#"):
			l = strings.Replace(l, "#", `\#`, 1)
		case t != "" && (strings.Trim(t, "=") == "" || strings.Trim(t, "-") == ""):
			l = `\` + t // 避免被解析为 setext 标题
		}
		lines[i] = escapeText(l)
	}
	return strings.Join(lines, "\n")
}

// inline 把注释压缩为表格单元格可用的单行文本：段落之间用 <br> 分隔。
func inline(c string) string {
	if c == "" {
		return ""
	}
	var paras []string
	for _, p := range strings.Split(c, "\n\n") {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			paras = append(paras, escapeText(p))
		}
	}
	return strings.Join(paras, "<br>")
}

// summary 取注释的第一段，用作简介。
func summary(c string) string {
	p, _, _ := strings.Cut(c, "\n\n")
	return strings.Join(strings.Fields(p), " ")
}

// escapeText 转义代码片段之外的尖括号。
func escapeText(s string) string {
	if !strings.ContainsAny(s, "<>") {
		return s
	}
	var b strings.Builder
	inCode := false
	for _, r := range s {
		switch {
		case r == '`':
			inCode = !inCode
			b.WriteRune(r)
		case r == '<' && !inCode:
			b.WriteString("&lt;")
		case r == '>' && !inCode:
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// code 生成行内代码；内容含反引号时加长定界符。
func code(s string) string {
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	d := strings.Repeat("`", longestRun(s, '`')+1)
	return d + " " + s + " " + d
}

// fence 返回不会与内容冲突的代码块定界符。
func fence(s string) string { return strings.Repeat("`", max(3, longestRun(s, '`')+1)) }

func longestRun(s string, c byte) int {
	best, n := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
}
