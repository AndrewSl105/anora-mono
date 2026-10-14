// Command anora is the Anora Runner service and its CLI helpers.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/andrewsl105/anora-runner/internal/auth"
	"github.com/andrewsl105/anora-runner/internal/config"
	"github.com/andrewsl105/anora-runner/internal/store"
)

const usage = `usage: anora <command>

commands:
  serve           run the service (default)
  migrate         apply database migrations and exit
  hash-password   read a password and print its bcrypt hash (ADMIN_PASSWORD_HASH)
  totp-setup      print a new TOTP secret (ADMIN_TOTP_SECRET) and its otpauth:// URI
`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = runServe()
	case "migrate":
		err = runMigrate()
	case "hash-password":
		err = hashPassword()
	case "totp-setup":
		err = totpSetup()
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("fatal", "cmd", cmd, "err", err)
		os.Exit(1)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runMigrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Migrate(ctx)
}

func hashPassword() error {
	pw, err := readSecret("Password: ")
	if err != nil {
		return err
	}
	if len(pw) < 12 {
		return fmt.Errorf("use at least 12 characters")
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		again, err := readSecret("Repeat: ")
		if err != nil {
			return err
		}
		if again != pw {
			return fmt.Errorf("passwords do not match")
		}
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

// readSecret reads without echo from a terminal, or one line from a pipe.
func readSecret(prompt string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func totpSetup() error {
	secret, uri, err := auth.NewTOTP("owner")
	if err != nil {
		return err
	}
	fmt.Printf("ADMIN_TOTP_SECRET=%s\n\nAdd this to your authenticator app (or render it as a QR code):\n%s\n", secret, uri)
	return nil
}
