package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	tty *os.File
	rd  *bufio.Reader
}

var _ auth.UserAuthenticator = (*ttyAuth)(nil)

func newTTYAuth() (*ttyAuth, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("no controlling terminal (run this in a terminal): %w", err)
	}
	return &ttyAuth{tty: f, rd: bufio.NewReader(f)}, nil
}

func (t *ttyAuth) Close() error { return t.tty.Close() }

func (t *ttyAuth) readLine(prompt string) (string, error) {
	fmt.Fprint(t.tty, prompt)
	line, err := t.rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readSecret reads a line with terminal echo off, and puts echo back on every
// exit path including Ctrl-C.
func (t *ttyAuth) readSecret(prompt string) (string, error) {
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
			fmt.Fprintln(t.tty)
			os.Exit(130)
		}
	}()

	line, err := t.readLine(prompt)
	fmt.Fprintln(t.tty)
	return line, err
}

func (t *ttyAuth) Phone(context.Context) (string, error) {
	return t.readLine("Phone number with country code (e.g. +84901234567): ")
}

func (t *ttyAuth) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	return t.readSecret("Login code Telegram just sent you: ")
}

func (t *ttyAuth) Password(context.Context) (string, error) {
	return t.readSecret("Two-step verification password: ")
}

func (t *ttyAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("refusing to accept Telegram's terms of service on your behalf; sign in with the official app first")
}

func (t *ttyAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("this phone number has no Telegram account; refusing to create one")
}
