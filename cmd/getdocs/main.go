// getdocs 是基于 git 仓库的文档服务。
//
//	getdocs [serve] [-config getdocs.yaml]          运行 Web / API / MCP 服务与定时同步
//	getdocs sync [-force] [ns/tree ...]             同步一次后退出
//	getdocs mcp [-allow-sync] [-refresh 30s]        以 stdio 运行 MCP 服务
//	getdocs version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rushteam/beauty/pkg/foundation/buildinfo"
	blog "github.com/rushteam/beauty/pkg/service/logger"

	"github.com/rushteam/getdocs/internal/bootstrap"
	"github.com/rushteam/getdocs/internal/domain/docs"
	"github.com/rushteam/getdocs/internal/infra/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("getdocs "+cmd, flag.ContinueOnError)
	cfgFile := fs.String("config", envOr("GETDOCS_CONFIG", "getdocs.yaml"), "config file")
	logLevel := fs.String("log-level", envOr("GETDOCS_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	logJSON := fs.Bool("log-json", false, "log in JSON format")
	var (
		force     *bool
		allowSync *bool
		refresh   *time.Duration
	)
	switch cmd {
	case "serve":
	case "sync":
		force = fs.Bool("force", false, "rebuild even if the commit is unchanged")
	case "mcp":
		allowSync = fs.Bool("allow-sync", false, "expose the sync_tree tool")
		refresh = fs.Duration("refresh", 30*time.Second, "reload snapshots synced by other processes at this interval (0 disables)")
	case "version":
		fmt.Println(buildinfo.Get().String())
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// 日志始终写 stderr：stdio MCP 模式下 stdout 是协议通道。
	logger, err := newLogger(*logLevel, *logJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*cfgFile)
	if err != nil {
		logger.ErrorContext(ctx, "load config", "err", err)
		return 1
	}
	app, err := bootstrap.Build(cfg, logger)
	if err != nil {
		logger.ErrorContext(ctx, "init", "err", err)
		return 1
	}

	switch cmd {
	case "serve":
		if err := app.Serve(ctx); err != nil {
			logger.ErrorContext(ctx, "serve", "err", err)
			return 1
		}
	case "sync":
		var keys []docs.TreeKey
		for _, a := range fs.Args() {
			k, err := docs.ParseTreeKey(a)
			if err != nil {
				logger.ErrorContext(ctx, "invalid argument", "err", err)
				return 2
			}
			keys = append(keys, k)
		}
		results := app.SyncOnce(ctx, keys, *force)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		for _, r := range results {
			if r.Err != "" {
				return 1
			}
		}
	case "mcp":
		if err := app.ServeStdio(ctx, *allowSync || cfg.MCP.AllowSync, *refresh); err != nil {
			logger.ErrorContext(ctx, "mcp", "err", err)
			return 1
		}
	}
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: getdocs <command> [flags]

commands:
  serve     run the web UI, HTTP API, MCP endpoint and scheduled sync (default)
  sync      sync the given trees (namespace/tree), or all trees, then exit
  mcp       serve MCP over stdio for local agents
  version   print the version

common flags:
  -config FILE      config file (env GETDOCS_CONFIG, default getdocs.yaml)
  -log-level LEVEL  debug|info|warn|error
  -log-json         log in JSON format
`)
}

// newLogger 创建写 stderr 的日志器。级别由 beauty 的全局 LevelVar 控制，框架内部日志与业务日志
// 使用同一级别，并可在运行时通过调试端口的 /debug/loglevel 调整；有 trace 时自动附带 trace_id / span_id。
func newLogger(level string, json bool) (*slog.Logger, error) {
	if err := blog.SetLevelByName(level); err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: levelFunc(blog.GetLevel)}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if json {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(blog.NewTraceHandler(h)), nil
}

// levelFunc 让 slog 每次判断级别时读取当前值，使运行时调整立即生效。
type levelFunc func() slog.Level

func (f levelFunc) Level() slog.Level { return f() }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
