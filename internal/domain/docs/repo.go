package docs

import (
	"net/url"
	"regexp"
	"strings"
)

// NamePattern 是命名空间与文档树名称的规则，名称会出现在 URL 与存储路径中。
const NamePattern = `^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`

var nameRe = regexp.MustCompile(NamePattern)

// ValidName 判断命名空间或文档树名称是否合法。
func ValidName(s string) bool { return nameRe.MatchString(s) }

var sshRepoRe = regexp.MustCompile(`^(?:ssh://)?[\w.-]+@([^:/]+)(?::\d+/|[:/])(.+?)(?:\.git)?/?$`)

// ParseRepo 从 https / ssh / scp 风格的仓库地址中取出主机名与项目路径，无法识别时返回空。
func ParseRepo(repo string) (host, project string) {
	switch {
	case strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "http://"):
		u, err := url.Parse(repo)
		if err != nil {
			return "", ""
		}
		host, project = u.Hostname(), strings.Trim(u.Path, "/")
	default:
		m := sshRepoRe.FindStringSubmatch(repo)
		if m == nil {
			return "", ""
		}
		host, project = m[1], m[2]
	}
	return strings.ToLower(host), strings.TrimSuffix(project, ".git")
}

// DetectProvider 按仓库域名识别托管平台，无法识别时返回空。
func DetectProvider(repo string) Provider {
	host, _ := ParseRepo(repo)
	switch {
	case host == "":
		return ""
	case strings.Contains(host, "github"):
		return ProviderGitHub
	case strings.Contains(host, "gitlab"):
		return ProviderGitLab
	case strings.Contains(host, "gitee"):
		return ProviderGitee
	case strings.Contains(host, "gitea"), strings.Contains(host, "codeberg"):
		return ProviderGitea
	case strings.Contains(host, "bitbucket"):
		return ProviderBitbucket
	}
	return ""
}

// DefaultTokenUser 是各平台 HTTPS 令牌认证可用的默认用户名。
// GitHub / GitLab / Gitea 根据令牌识别用户，用户名只需非空；Gitee 必须使用真实用户名，没有默认值。
func DefaultTokenUser(p Provider) string {
	switch p {
	case ProviderGitHub:
		return "x-access-token"
	case ProviderBitbucket:
		return "x-token-auth" // 仓库 / 项目访问令牌；App password 需填写真实用户名
	case ProviderGitee:
		return ""
	}
	return "oauth2"
}

// InferBrowseURL 根据仓库地址与托管平台推导文件浏览模板；平台为空时按域名识别，
// 仍无法识别的按 GitHub 规则生成。
func InferBrowseURL(repo string, p Provider) string {
	host, project := ParseRepo(repo)
	if host == "" || project == "" {
		return ""
	}
	if p == "" {
		p = DetectProvider(repo)
	}
	base := "https://" + host + "/" + project
	switch p {
	case ProviderGitLab:
		return base + "/-/blob/{branch}/{path}"
	case ProviderBitbucket:
		return base + "/src/{branch}/{path}"
	case ProviderGitea:
		return base + "/src/branch/{branch}/{path}"
	default: // GitHub、Gitee 及兼容 GitHub URL 规则的平台
		return base + "/blob/{branch}/{path}"
	}
}

// IsRemoteRepo 判断地址是否为 https 或 ssh（含 scp 风格）的远程 git 仓库。
func IsRemoteRepo(repo string) bool {
	if strings.HasPrefix(repo, "-") || strings.ContainsAny(repo, " \t\r\n") {
		return false
	}
	if strings.HasPrefix(repo, "https://") {
		u, err := url.Parse(repo)
		return err == nil && u.Hostname() != "" && u.User == nil && strings.Trim(u.Path, "/") != ""
	}
	host, project := ParseRepo(repo)
	// 以 - 开头的主机名会被 ssh 当作命令行选项
	return host != "" && !strings.HasPrefix(host, "-") && project != "" && !strings.HasPrefix(repo, "http://")
}

// RepoHost 返回仓库地址中的主机名（小写），无法识别时返回空。
func RepoHost(repo string) string {
	host, _ := ParseRepo(repo)
	return host
}
