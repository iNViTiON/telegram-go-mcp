package tgclient

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/session"
)

// atomicStorage persists the MTProto session without ever exposing a torn file.
//
// gotd's session.FileStorage uses os.WriteFile, which truncates and then
// writes. Several server processes can share one session file (the desktop
// app starts more than one), so a reader could see a half-written file and
// lose the session. Here a write goes to a private temp file in the same
// directory, is synced, then renamed over the target: readers see the old file
// or the new one, never a partial one. CreateTemp makes the file 0600.
type atomicStorage struct {
	path string
	mu   sync.Mutex
}

var _ session.Storage = (*atomicStorage)(nil)

func (s *atomicStorage) LoadSession(context.Context) ([]byte, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, session.ErrNotFound
	}
	return data, err
}

func (s *atomicStorage) StoreSession(_ context.Context, data []byte) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.CreateTemp(filepath.Dir(s.path), ".session-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
