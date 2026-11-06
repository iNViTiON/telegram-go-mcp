package instancelock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSecondAcquireIsRefusedUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire: want ErrHeld, got %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("double release must be harmless: %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	second.Release()
}
