package tgclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/session"
)

func TestAtomicStorageRoundTrip(t *testing.T) {
	s := &atomicStorage{path: filepath.Join(t.TempDir(), "session.json")}
	ctx := context.Background()

	if _, err := s.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("missing file: want session.ErrNotFound, got %v", err)
	}
	if err := s.StoreSession(ctx, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSession(ctx)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("got %q, %v", got, err)
	}
	st, _ := os.Stat(s.path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("session file is %#o, want 0600", st.Mode().Perm())
	}
}

// Two independent storages model two server processes sharing one file. A
// reader runs throughout; every read must be complete valid JSON.
func TestAtomicStorageNeverTorn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	ctx := context.Background()

	seed := &atomicStorage{path: path}
	if err := seed.StoreSession(ctx, mustJSON(0, 1)); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var torn atomic.Int64
	var reads atomic.Int64
	var rd sync.WaitGroup
	rd.Add(1)
	go func() {
		defer rd.Done()
		r := &atomicStorage{path: path}
		for !stop.Load() {
			data, err := r.LoadSession(ctx)
			if err != nil {
				torn.Add(1)
				continue
			}
			reads.Add(1)
			if !json.Valid(data) {
				torn.Add(1)
			}
		}
	}()

	var wr sync.WaitGroup
	for w := 0; w < 4; w++ {
		wr.Add(1)
		go func(w int) {
			defer wr.Done()
			s := &atomicStorage{path: path} // separate mutex: a separate "process"
			for i := 0; i < 150; i++ {
				// Alternate sizes so a shorter write over a longer one would leave a tail.
				if err := s.StoreSession(ctx, mustJSON(w*1000+i, 1+(i%2)*200000)); err != nil {
					t.Errorf("store: %v", err)
					return
				}
			}
		}(w)
	}
	wr.Wait()
	stop.Store(true)
	rd.Wait()

	if reads.Load() == 0 {
		t.Fatal("reader never ran")
	}
	if n := torn.Load(); n != 0 {
		t.Fatalf("%d of %d reads saw a torn or missing file", n, reads.Load())
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("final file is %#o, want 0600", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".session-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func mustJSON(n, padLen int) []byte {
	return []byte(fmt.Sprintf(`{"n":%d,"pad":%q}`, n, strings.Repeat("x", padLen)))
}
