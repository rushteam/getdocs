// Package bootstrap 是组合根：按配置创建基础设施、装配用例与入站适配器。
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rushteam/beauty/pkg/foundation/buildinfo"

	mcpadapter "github.com/rushteam/getdocs/internal/adapter/mcp"
	"github.com/rushteam/getdocs/internal/application/catalog"
	"github.com/rushteam/getdocs/internal/application/search"
	"github.com/rushteam/getdocs/internal/domain/docs"
	"github.com/rushteam/getdocs/internal/infra/config"
	"github.com/rushteam/getdocs/internal/infra/git"
	"github.com/rushteam/getdocs/internal/infra/hooks"
	"github.com/rushteam/getdocs/internal/infra/markdown"
	"github.com/rushteam/getdocs/internal/infra/protodoc"
	"github.com/rushteam/getdocs/internal/infra/storage"
)

// App 持有装配好的组件。
type App struct {
	Config   *config.Config
	Logger   *slog.Logger
	Catalog  *catalog.Service
	Searcher docs.Searcher // 未启用搜索时为 nil
	Markdown *markdown.Engine
}

// Build 创建存储、git 拉取器、解析器与同步服务，并注册扩展钩子。不会执行同步。
func Build(cfg *config.Config, logger *slog.Logger) (*App, error) {
	store, err := storage.NewFS(cfg.Storage.Dir)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	fetcher, err := git.NewFetcher(filepath.Join(cfg.Storage.Dir, "repos"), cfg.Sync.GitTimeout.Std(), logger)
	if err != nil {
		return nil, fmt.Errorf("git: %w", err)
	}
	md := markdown.New()
	deps := catalog.Deps{
		Fetcher: fetcher, Parser: protodoc.NewParser(md), Snapshots: store, States: store, Logger: logger,
	}
	if *cfg.Registry.Enabled && cfg.Server.AdminToken != "" {
		deps.Registry = storage.NewFileRegistry(filepath.Join(cfg.Storage.Dir, "registry.json"))
	}
	svc := catalog.New(deps, catalog.Options{
		KeepVersions:    cfg.Storage.KeepVersions,
		SyncTimeout:     cfg.Sync.Timeout.Std(),
		Concurrency:     cfg.Sync.Concurrency,
		DefaultInterval: cfg.Sync.Interval.Std(),
		Credentials:     cfg.DomainCredentials(),
	}, cfg.DomainNamespaces())
	if err := svc.LoadRegistry(context.Background()); err != nil { // 启动阶段，尚无生命周期 ctx
		return nil, err
	}

	app := &App{Config: cfg, Logger: logger, Catalog: svc, Markdown: md}
	if *cfg.Search.Enabled {
		ix := search.NewIndex()
		svc.AddHook(ix)
		app.Searcher = ix
	}
	if ce := cfg.Hooks.ChunkExport; ce != nil && ce.Dir != "" {
		svc.AddHook(&hooks.ChunkExporter{Dir: ce.Dir, ChunkSize: cfg.Hooks.ChunkSize})
	}
	for _, w := range cfg.Hooks.Webhooks {
		svc.AddHook(&hooks.Webhook{URL: w.URL, Secret: w.Secret, WithChunks: w.WithChunks, ChunkSize: cfg.Hooks.ChunkSize})
	}
	return app, nil
}

// MCPServer 创建 MCP 服务器。allowSync 为 true 时注册有副作用的 sync_tree 工具，同步绑定到 lifecycle。
func (a *App) MCPServer(lifecycle context.Context, allowSync bool) *mcp.Server {
	s := mcpadapter.New(a.Catalog, a.Searcher, a.Config.Server.BaseURL, buildinfo.Get().Version)
	if allowSync {
		s.RegisterSync(lifecycle)
	}
	return s.MCPServer()
}

// SyncOnce 同步指定文档树（为空表示全部）后返回，用于命令行与 CI。
func (a *App) SyncOnce(ctx context.Context, keys []docs.TreeKey, force bool) []catalog.SyncResult {
	a.Catalog.Restore(ctx)
	return a.Catalog.SyncAll(ctx, keys, docs.TriggerCLI, force)
}

// ServeStdio 以 stdio 运行 MCP 服务。默认只读，跟随存储中其他进程（serve）的同步结果；
// allowSync 为 true 时也可由 agent 触发同步。
func (a *App) ServeStdio(ctx context.Context, allowSync bool, refresh time.Duration) error {
	a.Catalog.Restore(ctx)
	if refresh > 0 {
		go func() {
			t := time.NewTicker(refresh)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := a.Catalog.LoadRegistry(ctx); err != nil {
						a.Logger.WarnContext(ctx, "reload registry failed", "err", err)
					}
					a.Catalog.Restore(ctx)
				}
			}
		}()
	}
	err := a.MCPServer(ctx, allowSync).Run(ctx, &mcp.StdioTransport{})
	if ctx.Err() != nil {
		return nil
	}
	return err
}
