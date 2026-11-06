package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func pipeAuth(t *testing.T) (*ttyAuth, *os.File) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() })
	return &ttyAuth{out: io.Discard, rd: bufio.NewReader(pr)}, pw
}

func TestReadLineReturnsInput(t *testing.T) {
	a, w := pipeAuth(t)
	if _, err := w.WriteString("  +10000000000 \n"); err != nil {
		t.Fatal(err)
	}
	got, err := a.readLine(context.Background(), "phone: ")
	if err != nil || got != "+10000000000" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadLineHonoursCancelWithoutInput(t *testing.T) {
	a, _ := pipeAuth(t) // nothing is ever written: a blocking read would hang
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := a.readLine(ctx, "phone: ")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel was not prompt")
	}
}
