package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rushteam/beauty"
	"github.com/rushteam/beauty/pkg/foundation/buildinfo"
	"github.com/rushteam/beauty/pkg/foundation/signals"
	"github.com/rushteam/beauty/pkg/middleware/accesslog"
	"github.com/rushteam/beauty/pkg/middleware/compress"
	"github.com/rushteam/beauty/pkg/middleware/health"
	"github.com/rushteam/beauty/pkg/middleware/recovery"
	"github.com/rushteam/beauty/pkg/middleware/requestid"
	"github.com/rushteam/beauty/pkg/service/logger"
	"github.com/rushteam/beauty/pkg/service/pprof"
	"github.com/rushteam/beauty/pkg/service/telemetry"
	"github.com/rushteam/beauty/pkg/service/webserver"
	"go.opentelemetry.io/otel/attribute"

	"getdocs/internal/adapter/web"
	"getdocs/internal/application/catalog"
)

const (
	shutdownTimeout = 15 * time.Second
	compressMinSize = 1024 // 小响应压缩收益低于开销
)

// Serve 以 beauty 应用运行 HTTP 服务、定时同步与可选的调试服务，直到 ctx 取消后优雅退出。
// 任一服务启动失败（如端口被占用）都会停止整个应用并返回该错误。
func (a *App) Serve(ctx context.Context) error {
	cfg := a.Config
	// 后台同步绑定到 lifecycle：信号到来或某个服务异常退出时都会被取消。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Catalog.Restore(ctx)

	var app *beauty.App
	hs, err := a.httpServer(ctx, func() error {
		if !app.Ready() {
			return errors.New("shutting down")
		}
		return a.ready()
	})
	if err != nil {
		return err
	}
	services := []*tracked{
		{Service: hs},
		{Service: schedulerService{catalog.NewScheduler(a.Catalog, *cfg.Sync.OnStart)}},
	}
	if cfg.Server.DebugAddr != "" {
		registerDebugRoutes()
		services = append(services, &tracked{Service: pprof.New(pprof.WithAddr(cfg.Server.DebugAddr))})
	}
	var opts []beauty.Option
	if cfg.Telemetry.Enabled {
		if opts, err = telemetryOptions(); err != nil {
			return err
		}
	}
	for _, s := range services {
		opts = append(opts, beauty.WithService(s))
	}

	info := buildinfo.Get()
	a.Logger.InfoContext(ctx, "starting getdocs", "addr", cfg.Server.Addr, "debug_addr", cfg.Server.DebugAddr,
		"telemetry", cfg.Telemetry.Enabled, "version", info.Version, "commit", info.Short())
	app = beauty.New(opts...)
	if err := app.Start(ctx); err != nil {
		return err
	}
	cancel()

	// 生命周期已结束：脱离取消等待进行中的同步收尾，但限定时间，避免卡死退出。
	waitCtx, done := signals.DetachTimeout(ctx, shutdownTimeout)
	defer done()
	if err := a.Catalog.Wait(waitCtx); err != nil {
		a.Logger.WarnContext(waitCtx, "in-flight syncs did not finish", "err", err)
	}
	var errs []error
	for _, s := range services {
		if s.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.String(), s.err))
		}
	}
	return errors.Join(errs...)
}

// httpServer 装配站点、API 与 MCP 处理器，并套上进程级中间件（从外到内）：
// 请求 ID → panic 兜底 → 健康检查 → 访问日志 → gzip 压缩。健康检查放在访问日志之外，
// 避免探针请求刷屏。
func (a *App) httpServer(lifecycle context.Context, ready health.ReadinessCheck) (*webserver.Server, error) {
	cfg := a.Config
	opts := web.Options{
		Title:         cfg.Server.Title,
		BaseURL:       cfg.Server.BaseURL,
		AdminToken:    cfg.Server.AdminToken,
		WebhookSecret: cfg.Server.WebhookSecret,
		Lifecycle:     lifecycle,
		Logger:        a.Logger,
		AllowedHosts:  cfg.Registry.AllowedHosts,
	}
	if *cfg.MCP.Enabled {
		srv := a.MCPServer(lifecycle, cfg.MCP.AllowSync)
		opts.MCP = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
			&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: a.Logger})
		opts.MCPToken = cfg.MCP.Token
	}
	ws, err := web.New(a.Catalog, a.Searcher, a.Markdown, opts)
	if err != nil {
		return nil, err
	}
	hs := webserver.New(cfg.Server.Addr, ws.Handler(),
		webserver.WithServiceName("getdocs"),
		webserver.WithVersion(buildinfo.Get().Version),
		webserver.WithIdleTimeout(2*time.Minute),
		webserver.WithShutdownTimeout(shutdownTimeout),
		webserver.WithMiddleware(
			requestid.HTTPMiddleware,
			recovery.HTTPMiddleware(recovery.WithOnPanic(func(ctx context.Context, p any, stack []byte) {
				a.Logger.ErrorContext(ctx, "panic in handler", "panic", p,
					"request_id", requestid.FromContext(ctx), "stack", string(stack))
			})),
			health.Middleware(ready),
			accesslog.HTTPMiddleware,
			compress.Middleware(compressMinSize),
		),
	)
	// 不设整体读写超时（MCP 与大文档需要长响应），只限制请求头读取以防 slow-loris。
	hs.ReadHeaderTimeout = 10 * time.Second
	return hs, nil
}

// ready 在至少一棵文档树有可用快照后就绪：首次同步完成前站点没有内容可提供。
func (a *App) ready() error {
	specs := a.Catalog.Specs()
	for _, spec := range specs {
		if _, err := a.Catalog.Snapshot(spec.Key); err == nil {
			return nil
		}
	}
	if len(specs) == 0 {
		return nil
	}
	return errors.New("no documentation tree has been synced yet")
}

// telemetryOptions 启用 OTLP/HTTP 的 trace 与指标上报，导出端点读取 OTEL_EXPORTER_OTLP_* 环境变量。
func telemetryOptions() (opts []beauty.Option, err error) {
	defer func() {
		// 导出器配置有误时 beauty 会 panic，这里转成启动错误。
		if p := recover(); p != nil {
			err = fmt.Errorf("telemetry: %v", p)
		}
	}()
	version := attribute.String("service.version", buildinfo.Get().Version)
	return []beauty.Option{
		beauty.WithTrace(telemetry.WithTraceServiceName("getdocs", version), telemetry.WithTraceOTLPHTTPExporter()),
		beauty.WithMetric(telemetry.WithMetricServiceName("getdocs", version), telemetry.WithMetricOTLPHTTPReader()),
	}, nil
}

var debugRoutesOnce sync.Once

// registerDebugRoutes 在 pprof 服务使用的 DefaultServeMux 上补充日志级别与构建信息接口。
func registerDebugRoutes() {
	debugRoutesOnce.Do(func() {
		http.Handle("/debug/loglevel", logger.LevelHandler())
		http.Handle("/debug/version", buildinfo.Handler())
	})
}

// tracked 记录服务的退出错误：beauty 遇到服务出错只记录日志并停止应用，Serve 需要据此返回失败。
// Start 在 beauty.App.Start 返回前结束，读取 err 无需加锁。包装后不再暴露 Ready()，它只用于注册中心，
// 本服务未使用。
type tracked struct {
	beauty.Service
	err error
}

func (t *tracked) Start(ctx context.Context) error {
	t.err = t.Service.Start(ctx)
	return t.err
}

// schedulerService 把定时同步作为 beauty 服务运行，Run 阻塞到停机。
type schedulerService struct{ *catalog.Scheduler }

func (s schedulerService) Start(ctx context.Context) error {
	s.Run(ctx)
	return nil
}

func (schedulerService) String() string { return "scheduler" }
