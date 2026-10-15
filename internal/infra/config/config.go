// Package config 加载 YAML 配置。配置值支持 ${ENV} / ${ENV:-default} 环境变量展开，
// 便于把仓库令牌等敏感信息放在环境变量中。
package config

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// Config 是服务配置根。
type Config struct {
	Server     Server      `yaml:"server"`
	Storage    Storage     `yaml:"storage"`
	Sync       Sync        `yaml:"sync"`
	Search     Search      `yaml:"search"`
	MCP        MCP         `yaml:"mcp"`
	Hooks      Hooks       `yaml:"hooks"`
	Telemetry  Telemetry   `yaml:"telemetry"`
	Namespaces []Namespace `yaml:"namespaces"`
	// Credentials 是具名的仓库凭据，供页面登记的文档树按名称引用，令牌不会经过浏览器。
	Credentials []Credential `yaml:"credentials"`
	Registry    Registry     `yaml:"registry"`
}

type Credential struct {
	Name string `yaml:"name"`
	Auth `yaml:",inline"`
}

// Registry 控制通过页面 / API 登记文档树的能力，需同时配置 server.admin_token。
type Registry struct {
	Enabled *bool `yaml:"enabled"` // 默认 true（仍需 admin_token）
	// AllowedHosts 是允许登记的 git 主机名；为空时允许任意公网主机，内网地址始终需要显式列出。
	AllowedHosts []string `yaml:"allowed_hosts"`
}

type Server struct {
	Addr    string `yaml:"addr"`
	Title   string `yaml:"title"`
	BaseURL string `yaml:"base_url"` // 对外访问地址，用于 MCP / API 返回可点击链接
	// AdminToken 保护同步、webhook 等写操作；为空时这些接口不鉴权（仅建议在内网使用）。
	AdminToken string `yaml:"admin_token"`
	// WebhookSecret 用于校验 GitHub / GitLab / Gitee / Gitea 推送事件；为空时退回使用 AdminToken。
	WebhookSecret string `yaml:"webhook_secret"`
	// DebugAddr 非空时在该地址提供 /debug/pprof/ 与 /debug/loglevel，不鉴权，应只监听本机。
	DebugAddr string `yaml:"debug_addr"`
}

type Telemetry struct {
	// Enabled 为 true 时通过 OTLP/HTTP 上报 trace 与指标；端点、鉴权头等使用标准
	// OTEL_EXPORTER_OTLP_* 环境变量配置。
	Enabled bool `yaml:"enabled"`
}

type Storage struct {
	Dir          string `yaml:"dir"`           // 数据目录：快照、状态、git 缓存
	KeepVersions int    `yaml:"keep_versions"` // 每棵文档树保留的快照数
}

type Sync struct {
	Interval    Duration `yaml:"interval"`    // 默认同步间隔，可被文档树覆盖
	OnStart     *bool    `yaml:"on_start"`    // 启动时同步一次，默认 true
	Timeout     Duration `yaml:"timeout"`     // 单次同步超时
	GitTimeout  Duration `yaml:"git_timeout"` // 单条 git 命令超时
	Concurrency int      `yaml:"concurrency"` // 批量同步并发度
}

type Search struct {
	Enabled *bool `yaml:"enabled"` // 默认 true
}

type MCP struct {
	Enabled *bool  `yaml:"enabled"` // 默认 true，挂载在 /mcp
	Token   string `yaml:"token"`   // 为空时不鉴权
	// AllowSync 为 true 时提供 sync_tree 工具，允许 agent 触发同步，默认关闭。
	AllowSync bool `yaml:"allow_sync"`
}

type Hooks struct {
	ChunkExport *ChunkExport `yaml:"chunk_export"`
	Webhooks    []Webhook    `yaml:"webhooks"`
	ChunkSize   int          `yaml:"chunk_size"` // 切片最大字符数
}

type ChunkExport struct {
	Dir string `yaml:"dir"`
}

type Webhook struct {
	URL        string `yaml:"url"`
	Secret     string `yaml:"secret"`
	WithChunks bool   `yaml:"with_chunks"`
}

type Namespace struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Trees       []Tree `yaml:"trees"`
}

type Tree struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Repo        string `yaml:"repo"`   // git 地址
	Path        string `yaml:"path"`   // 本地目录（与 repo 二选一）
	Branch      string `yaml:"branch"` // 默认为远端默认分支
	// Provider 是托管平台：github / gitlab / gitee / gitea / bitbucket，留空按域名识别；
	// 自建平台域名不含平台名时需要指定。
	Provider    string   `yaml:"provider"`
	Root        string   `yaml:"root"` // 文档根目录或单个文件，留空自动探测
	Include     []string `yaml:"include"`
	Exclude     []string `yaml:"exclude"`
	MaxFileSize int64    `yaml:"max_file_size"`
	Proto       bool     `yaml:"proto"` // 同时把 .proto 文件转换为接口文档
	Interval    Duration `yaml:"interval"`
	AutoSync    *bool    `yaml:"auto_sync"` // 默认 true
	Auth        Auth     `yaml:"auth"`
	// BrowseURL 是“查看源文件”链接模板；已知平台按 provider 自动推导。
	BrowseURL string `yaml:"browse_url"`
}

type Auth struct {
	// Username 是 HTTPS 令牌认证的用户名，留空按平台取默认值；Gitee 必须填写真实用户名。
	Username string `yaml:"username"`
	Token    string `yaml:"token"`
	SSHKey   string `yaml:"ssh_key"`
}

// Duration 支持 "10m"、"1h30m" 形式的 YAML 时长。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	if s == "" || s == "0" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

func expandEnv(s string) string {
	return envRe.ReplaceAllStringFunc(s, func(m string) string {
		g := envRe.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(g[1]); ok && v != "" {
			return v
		}
		return g[3]
	})
}

// Load 读取并校验配置文件，相对路径以配置文件所在目录为基准。
func Load(file string) (*Config, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	dec := yaml.NewDecoder(strings.NewReader(expandEnv(string(raw))))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	cfg.applyDefaults(filepath.Dir(file))
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", file, err)
	}
	return cfg, nil
}

func (c *Config) applyDefaults(baseDir string) {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.Title == "" {
		c.Server.Title = "GetDocs"
	}
	c.Server.BaseURL = strings.TrimSuffix(c.Server.BaseURL, "/")
	if c.Storage.Dir == "" {
		c.Storage.Dir = "data"
	}
	c.Storage.Dir = resolvePath(baseDir, c.Storage.Dir)
	if c.Storage.KeepVersions <= 0 {
		c.Storage.KeepVersions = 5
	}
	if c.Sync.Interval == 0 {
		c.Sync.Interval = Duration(15 * time.Minute)
	}
	if c.Sync.OnStart == nil {
		c.Sync.OnStart = ptr(true)
	}
	if c.Search.Enabled == nil {
		c.Search.Enabled = ptr(true)
	}
	if c.MCP.Enabled == nil {
		c.MCP.Enabled = ptr(true)
	}
	if c.Registry.Enabled == nil {
		c.Registry.Enabled = ptr(true)
	}
	for i := range c.Credentials {
		if k := &c.Credentials[i].SSHKey; *k != "" {
			*k = resolvePath(baseDir, *k)
		}
	}
	if c.Hooks.ChunkExport != nil && c.Hooks.ChunkExport.Dir != "" {
		c.Hooks.ChunkExport.Dir = resolvePath(baseDir, c.Hooks.ChunkExport.Dir)
	}
	for i := range c.Namespaces {
		ns := &c.Namespaces[i]
		if ns.Title == "" {
			ns.Title = ns.Name
		}
		for j := range ns.Trees {
			t := &ns.Trees[j]
			if t.Title == "" {
				t.Title = t.Name
			}
			if t.Interval == 0 {
				t.Interval = c.Sync.Interval
			}
			if t.AutoSync == nil {
				t.AutoSync = ptr(true)
			}
			if t.Path != "" {
				t.Path = resolvePath(baseDir, t.Path)
			}
			t.Provider = strings.ToLower(t.Provider)
			if t.Provider == "" && t.Repo != "" {
				t.Provider = string(docs.DetectProvider(t.Repo))
			}
			if t.Auth.SSHKey != "" {
				t.Auth.SSHKey = resolvePath(baseDir, t.Auth.SSHKey)
			}
		}
	}
}

func resolvePath(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return filepath.Join(base, p)
}

func (c *Config) validate() error {
	var errs []error
	seenCred := map[string]bool{}
	for _, cr := range c.Credentials {
		if !docs.ValidName(cr.Name) || seenCred[cr.Name] {
			errs = append(errs, fmt.Errorf("credential name %q must be unique and match %s", cr.Name, docs.NamePattern))
		}
		seenCred[cr.Name] = true
		if cr.Token == "" && cr.SSHKey == "" {
			errs = append(errs, fmt.Errorf("credential %s: token or ssh_key is required", cr.Name))
		}
	}
	seenNS := map[string]bool{}
	for _, ns := range c.Namespaces {
		if !docs.ValidName(ns.Name) {
			errs = append(errs, fmt.Errorf("namespace name %q must match %s", ns.Name, docs.NamePattern))
		}
		if seenNS[ns.Name] {
			errs = append(errs, fmt.Errorf("duplicate namespace %q", ns.Name))
		}
		seenNS[ns.Name] = true
		seenTree := map[string]bool{}
		for _, t := range ns.Trees {
			where := ns.Name + "/" + t.Name
			if !docs.ValidName(t.Name) {
				errs = append(errs, fmt.Errorf("tree name %q must match %s", where, docs.NamePattern))
			}
			if seenTree[t.Name] {
				errs = append(errs, fmt.Errorf("duplicate tree %q", where))
			}
			seenTree[t.Name] = true
			if (t.Repo == "") == (t.Path == "") {
				errs = append(errs, fmt.Errorf("tree %s: exactly one of repo or path is required", where))
			}
			if t.Provider != "" && !slices.Contains(docs.Providers, docs.Provider(t.Provider)) {
				errs = append(errs, fmt.Errorf("tree %s: unknown provider %q (github, gitlab, gitee, gitea, bitbucket)", where, t.Provider))
			}
			if t.Path != "" && (t.Provider != "" || t.Auth != (Auth{})) {
				errs = append(errs, fmt.Errorf("tree %s: provider and auth only apply to repo", where))
			}
			if t.Provider == string(docs.ProviderGitee) && t.Auth.Token != "" && t.Auth.Username == "" {
				errs = append(errs, fmt.Errorf("tree %s: gitee token auth requires auth.username (your gitee username)", where))
			}
			if err := (docs.Filter{Include: t.Include, Exclude: t.Exclude}).Validate(); err != nil {
				errs = append(errs, fmt.Errorf("tree %s: %w", where, err))
			}
			if t.Interval.Std() < 0 || (t.Interval.Std() > 0 && t.Interval.Std() < 10*time.Second) {
				errs = append(errs, fmt.Errorf("tree %s: interval must be at least 10s", where))
			}
		}
	}
	for _, w := range c.Hooks.Webhooks {
		if !strings.HasPrefix(w.URL, "http://") && !strings.HasPrefix(w.URL, "https://") {
			errs = append(errs, fmt.Errorf("webhook url %q must be http(s)", w.URL))
		}
	}
	return errors.Join(errs...)
}

// DomainCredentials 把具名凭据转换为领域模型。
func (c *Config) DomainCredentials() map[string]docs.Auth {
	out := map[string]docs.Auth{}
	for _, cr := range c.Credentials {
		out[cr.Name] = docs.Auth{Username: cr.Username, Password: cr.Token, SSHKey: cr.SSHKey}
	}
	return out
}

// DomainNamespaces 把配置转换为领域模型。
func (c *Config) DomainNamespaces() []*docs.Namespace {
	var out []*docs.Namespace
	for _, ns := range c.Namespaces {
		dn := &docs.Namespace{Name: ns.Name, Title: ns.Title, Description: ns.Description}
		for _, t := range ns.Trees {
			src := docs.Source{Kind: docs.SourceLocal, URL: t.Path, BrowseURL: t.BrowseURL}
			if t.Repo != "" {
				p := docs.Provider(t.Provider)
				src = docs.Source{Kind: docs.SourceGit, URL: t.Repo, Branch: t.Branch, Provider: p,
					Auth:      docs.Auth{Username: t.Auth.Username, Password: t.Auth.Token, SSHKey: t.Auth.SSHKey},
					BrowseURL: cmp.Or(t.BrowseURL, docs.InferBrowseURL(t.Repo, p))}
				if src.Auth.Username == "" && src.Auth.Password != "" {
					src.Auth.Username = docs.DefaultTokenUser(p)
				}
			}
			dn.Trees = append(dn.Trees, &docs.TreeSpec{
				Key:         docs.TreeKey{Namespace: ns.Name, Tree: t.Name},
				Title:       t.Title,
				Description: t.Description,
				Source:      src,
				Root:        t.Root,
				Filter:      docs.Filter{Include: t.Include, Exclude: t.Exclude, MaxFileSize: t.MaxFileSize, Proto: t.Proto},
				Interval:    t.Interval.Std(),
				AutoSync:    *t.AutoSync,
			})
		}
		out = append(out, dn)
	}
	return out
}

func ptr[T any](v T) *T { return &v }
