// Package hooks 提供内置的同步扩展：把每次同步的切片增量导出到文件，或推送到外部 HTTP 服务。
// 二者都是 RAG 流水线的接入方式——下游只需消费增量即可维护向量索引。
package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rushteam/beauty/pkg/resilience/backoff"

	"getdocs/internal/domain/docs"
)

// Payload 是导出与推送的数据格式。
type Payload struct {
	Event     string           `json:"event"` // "docs.synced"，文档树被删除时为 "docs.removed"
	Key       docs.TreeKey     `json:"key"`
	Version   string           `json:"version"`
	Revision  docs.Revision    `json:"revision"`
	Root      string           `json:"root"`
	Trigger   docs.Trigger     `json:"trigger,omitempty"`
	Changes   docs.ChangeSet   `json:"changes"`
	Chunks    *docs.ChunkDelta `json:"chunks,omitempty"`
	Timestamp time.Time        `json:"timestamp"`
}

func newPayload(ev docs.SyncEvent, withChunks bool, chunkSize int) Payload {
	p := Payload{
		Event: "docs.synced", Key: ev.Key, Version: ev.Current.Version, Revision: ev.Current.Revision,
		Root: ev.Current.Root, Trigger: ev.Run.Trigger, Changes: ev.Changes, Timestamp: time.Now().UTC(),
	}
	if withChunks {
		d := docs.DiffChunks(ev.Previous, ev.Current, chunkSize)
		p.Chunks = &d
	}
	return p
}

// ChunkExporter 把切片增量写入 <dir>/<namespace>/<tree>/<version>.json，供离线 RAG 任务消费。
type ChunkExporter struct {
	Dir       string
	ChunkSize int
}

func (e *ChunkExporter) Name() string { return "chunk-export" }

func (e *ChunkExporter) OnSync(_ context.Context, ev docs.SyncEvent) error {
	if ev.Restored || ev.Removed {
		return nil // 启动恢复不是新版本，导出过的增量无需重复写入
	}
	b, err := json.MarshalIndent(newPayload(ev, true, e.ChunkSize), "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(e.Dir, ev.Key.Namespace, ev.Key.Tree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+ev.Current.Version+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ev.Current.Version+".json"))
}

// Webhook 把同步事件 POST 到外部地址。配置 Secret 时以 HMAC-SHA256 签名，
// 签名放在 X-Getdocs-Signature: sha256=<hex> 头中。
type Webhook struct {
	URL        string
	Secret     string
	WithChunks bool
	ChunkSize  int
	Client     *http.Client
}

func (w *Webhook) Name() string { return "webhook:" + w.URL }

func (w *Webhook) OnSync(ctx context.Context, ev docs.SyncEvent) error {
	if ev.Restored {
		return nil
	}
	// 删除事件没有快照，只通知下游清理该文档树。
	p := Payload{Event: "docs.removed", Key: ev.Key, Timestamp: time.Now().UTC()}
	if !ev.Removed {
		p = newPayload(ev, w.WithChunks, w.ChunkSize)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return webhookRetry.RetryIf(ctx, func(ctx context.Context) error { return w.post(ctx, body) }, retryable)
}

// webhookRetry 最多重试 2 次，间隔约 1s、2s（带抖动），避免接收方短暂不可用时丢事件。
var webhookRetry = backoff.New(
	backoff.WithBase(time.Second),
	backoff.WithMax(10*time.Second),
	backoff.WithMaxRetries(2),
	backoff.WithJitter(backoff.JitterEqual),
)

// statusError 是接收方返回的非 2xx 响应。
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

// retryable 判断失败是否值得重试：4xx（408、429 除外）说明请求本身有误，重试无益。
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var se *statusError
	if errors.As(err, &se) && se.code >= 400 && se.code < 500 {
		return se.code == http.StatusRequestTimeout || se.code == http.StatusTooManyRequests
	}
	return true
}

func (w *Webhook) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "getdocs")
	if w.Secret != "" {
		mac := hmac.New(sha256.New, []byte(w.Secret))
		mac.Write(body)
		req.Header.Set("X-Getdocs-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &statusError{code: resp.StatusCode, msg: fmt.Sprintf("webhook %s: %s: %s", w.URL, resp.Status, bytes.TrimSpace(msg))}
	}
	return nil
}
