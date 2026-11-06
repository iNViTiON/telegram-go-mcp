package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"golang.org/x/sys/unix"
)

// ttyAuth asks for credentials on /dev/tty, never argv, env or stdin, so
// nothing sensitive can end up in shell history or a process listing.
type ttyAuth struct {
	tty *os.File // nil in tests; needed only to switch echo off
	out io.Writer
	rd  *bufio.Reader
}

var _ auth.UserAuthenticator = (*ttyAuth)(nil)

func newTTYAuth() (*ttyAuth, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("no controlling terminal (run this in a terminal): %w", err)
	}
	return &ttyAuth{tty: f, out: f, rd: bufio.NewReader(f)}, nil
}

func (t *ttyAuth) Close() error {
	if t.tty == nil {
		return nil
	}
	return t.tty.Close()
}

// readLine prompts and reads one line. It gives up as soon as ctx is done, so
// Ctrl-C works at a plain prompt instead of waiting for Enter. The reader
// goroutine is abandoned on cancel; the process is exiting anyway.
func (t *ttyAuth) readLine(ctx context.Context, prompt string) (string, error) {
	fmt.Fprint(t.out, prompt)

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := t.rd.ReadString('\n')
		ch <- result{strings.TrimSpace(line), err}
	}()

	select {
	case r := <-ch:
		return r.line, r.err
	case <-ctx.Done():
		fmt.Fprintln(t.out)
		return "", ctx.Err()
	}
}

// readSecret reads a line with terminal echo off, and puts echo back on every
// exit path including Ctrl-C.
func (t *ttyAuth) readSecret(ctx context.Context, prompt string) (string, error) {
	fd := int(t.tty.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", fmt.Errorf("cannot read terminal settings: %w", err)
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &quiet); err != nil {
		return "", fmt.Errorf("cannot disable echo: %w", err)
	}
	restore := func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, old) }
	defer restore()

	// Ctrl-C kills the process without running defers; restore echo first.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		if _, ok := <-sig; ok {
			restore()
			fmt.Fprintln(t.out)
			os.Exit(130)
		}
	}()

	line, err := t.readLine(ctx, prompt)
	fmt.Fprintln(t.out)
	return line, err
}

func (t *ttyAuth) Phone(ctx context.Context) (string, error) {
	return t.readLine(ctx, "Phone number with country code (e.g. +10000000000): ")
}

// Code is echoed on purpose: it is short-lived and also visible in your own
// Telegram app, and a typo typed blind fails the flow and triggers another
// auth.sendCode, which Telegram flood-waits.
func (t *ttyAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return t.readLine(ctx, "Login code Telegram just sent you: ")
}

// Password is the only secret typed blind.
func (t *ttyAuth) Password(ctx context.Context) (string, error) {
	return t.readSecret(ctx, "Two-step verification password: ")
}

func (t *ttyAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("refusing to accept Telegram's terms of service on your behalf; sign in with the official app first")
}

func (t *ttyAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("this phone number has no Telegram account; refusing to create one")
}
