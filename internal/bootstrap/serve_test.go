package bootstrap

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rushteam/getdocs/internal/domain/docs"
	"github.com/rushteam/getdocs/internal/infra/config"
)

func newTestApp(t *testing.T, addr string) *App {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# Install\n\n" + strings.Repeat("Run the installer and follow the prompts. ", 60) + "\n"
	if err := os.WriteFile(filepath.Join(src, "install.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgFile := filepath.Join(dir, "getdocs.yaml")
	yaml := "server:\n  addr: \"" + addr + "\"\nstorage:\n  dir: data\nsync:\n  on_start: false\n" +
		"namespaces:\n  - name: demo\n    trees:\n      - name: local\n        path: src\n        auto_sync: false\n"
	if err := os.WriteFile(cfgFile, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Build(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestHTTPMiddleware(t *testing.T) {
	app := newTestApp(t, ":0")
	ready := func() error { return app.ready() }
	hs, err := app.httpServer(t.Context(), ready)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hs.Handler)
	defer srv.Close()
	get := func(path string, header map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		// 关闭自动解压，以便检查 Content-Encoding。
		resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	if resp := get("/healthz", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d", resp.StatusCode)
	}
	if resp := get("/readyz", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz before first sync = %d, want 503", resp.StatusCode)
	}
	if _, err := app.Catalog.Sync(t.Context(), docs.TreeKey{Namespace: "demo", Tree: "local"}, docs.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	if resp := get("/readyz", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("/readyz after sync = %d, want 200", resp.StatusCode)
	}

	resp := get("/llms-full.txt", map[string]string{"X-Request-ID": "req-1", "Accept-Encoding": "gzip"})
	if got := resp.Header.Get("X-Request-ID"); got != "req-1" {
		t.Errorf("X-Request-ID = %q", got)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", resp.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); !strings.Contains(string(b), "Run the installer") {
		t.Errorf("unexpected body: %.200s", b)
	}
	if id := get("/", nil).Header.Get("X-Request-ID"); id == "" {
		t.Error("request ID not generated")
	}
}

func TestServeReturnsListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	app := newTestApp(t, ln.Addr().String())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := app.Serve(ctx); err == nil || ctx.Err() != nil {
		t.Errorf("Serve on a busy port: err = %v, ctx = %v", err, ctx.Err())
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	app := newTestApp(t, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}
