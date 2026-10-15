package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// FileRegistry 把登记的文档树保存在单个 JSON 文件中，实现 docs.TreeRegistry。
// 适合单副本部署；多副本或数据盘不持久时应换成共享存储实现。
type FileRegistry struct {
	path string
	mu   sync.Mutex
}

var _ docs.TreeRegistry = (*FileRegistry)(nil)

// NewFileRegistry 创建文件登记表，文件不存在时视为空。
func NewFileRegistry(path string) *FileRegistry { return &FileRegistry{path: path} }

type registryFile struct {
	Trees []docs.RegisteredTree `json:"trees"`
}

func (r *FileRegistry) Load(context.Context) ([]docs.RegisteredTree, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := os.ReadFile(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Trees, nil
}

func (r *FileRegistry) Save(_ context.Context, trees []docs.RegisteredTree) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := json.MarshalIndent(registryFile{Trees: trees}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	return writeAtomic(r.path, append(b, '\n'))
}
