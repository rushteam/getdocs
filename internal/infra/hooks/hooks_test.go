package hooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"getdocs/internal/domain/docs"
)

var key = docs.TreeKey{Namespace: "ns", Tree: "t"}

func event(t *testing.T) docs.SyncEvent {
	t.Helper()
	prev := &docs.Snapshot{Key: key, Version: "v1", Root: ".", Docs: []*docs.Document{
		{Path: "a.md", Title: "A", Hash: "1", Sections: []docs.Section{{Text: "old"}}},
		{Path: "gone.md", Title: "Gone", Hash: "2", Sections: []docs.Section{{Text: "bye"}}},
	}}
	prev.Seal()
	cur := &docs.Snapshot{Key: key, Version: "v2", Root: ".", Revision: docs.Revision{Commit: "abc"}, Docs: []*docs.Document{
		{Path: "a.md", Title: "A", Hash: "3", Sections: []docs.Section{{Text: "new"}}},
	}}
	cur.Seal()
	return docs.SyncEvent{Key: key, Previous: prev, Current: cur, Run: docs.SyncRun{Trigger: docs.TriggerWebhook},
		Changes: docs.Diff(prev, cur)}
}

func TestChunkExporter(t *testing.T) {
	dir := t.TempDir()
	e := &ChunkExporter{Dir: dir}
	if err := e.OnSync(t.Context(), event(t)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ns", "t", "v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != "docs.synced" || p.Version != "v2" || p.Revision.Commit != "abc" || p.Trigger != docs.TriggerWebhook {
		t.Errorf("payload = %+v", p)
	}
	if p.Chunks == nil || len(p.Chunks.Upsert) != 1 || len(p.Chunks.Delete) != 1 || p.Chunks.FromVersion != "v1" {
		t.Errorf("chunks = %+v", p.Chunks)
	}

	restored := event(t)
	restored.Restored = true
	restored.Current.Version = "v3"
	if err := e.OnSync(t.Context(), restored); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ns", "t", "v3.json")); !os.IsNotExist(err) {
		t.Errorf("restored event exported: %v", err)
	}
}

func TestWebhook(t *testing.T) {
	var calls atomic.Int32
	var got Payload
	var sig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if calls.Add(1) == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		sig = r.Header.Get("X-Getdocs-Signature")
		mac := hmac.New(sha256.New, []byte("s3cret"))
		mac.Write(body)
		if sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	w := &Webhook{URL: srv.URL, Secret: "s3cret", Client: srv.Client()}
	if err := w.OnSync(t.Context(), event(t)); err != nil {
		t.Fatalf("webhook with retry: %v", err)
	}
	if calls.Load() != 2 || got.Version != "v2" || got.Chunks != nil || len(got.Changes.Deleted) != 1 {
		t.Errorf("calls = %d, payload = %+v", calls.Load(), got)
	}

	w.WithChunks = true
	if err := w.OnSync(t.Context(), event(t)); err != nil {
		t.Fatal(err)
	}
	if got.Chunks == nil || len(got.Chunks.Upsert) != 1 {
		t.Errorf("chunks not pushed: %+v", got.Chunks)
	}
}

func TestWebhookCancelStopsRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	w := &Webhook{URL: srv.URL, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel() // 第一次失败后取消，重试等待应立即返回
		return srv.Client().Transport.RoundTrip(r)
	})}}
	if err := w.OnSync(ctx, event(t)); err == nil {
		t.Errorf("expected error")
	}
}

func TestWebhookClientErrorNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad signature", http.StatusUnauthorized)
	}))
	defer srv.Close()
	w := &Webhook{URL: srv.URL, Client: srv.Client()}
	if err := w.OnSync(t.Context(), event(t)); err == nil || calls.Load() != 1 {
		t.Errorf("err = %v, calls = %d, want error after a single call", err, calls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
