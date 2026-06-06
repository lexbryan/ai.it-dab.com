// Command createsuperuser bootstraps the first superuser so the system is
// usable from an empty database. It uses the same configuration loader as the
// server, so it runs identically on the host and inside the container.
//
// Usage:
//
//	# host (from ./backend, with env set, e.g. sourced from .env)
//	go run ./cmd/createsuperuser -email admin@example.com
//	# you are prompted for the password (not echoed)
//
//	# non-interactive (env)
//	SUPERUSER_EMAIL=admin@example.com SUPERUSER_PASSWORD=... \
//	  go run ./cmd/createsuperuser
//
//	# container
//	docker compose run --rm \
//	  -e SUPERUSER_EMAIL=admin@example.com -e SUPERUSER_PASSWORD=... \
//	  backend createsuperuser
//
// By default the command refuses to touch an existing account; pass
// -update-password to reset an existing user's password instead.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/auth"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/db"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/user"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "createsuperuser: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("createsuperuser", flag.ContinueOnError)
	emailFlag := fs.String("email", "", "superuser email (or SUPERUSER_EMAIL)")
	passwordFlag := fs.String("password", "", "superuser password (or SUPERUSER_PASSWORD; omit to be prompted)")
	updatePassword := fs.Bool("update-password", false, "reset the password if the user already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	email, err := parseEmail(firstNonEmpty(*emailFlag, os.Getenv("SUPERUSER_EMAIL")))
	if err != nil {
		return err
	}

	password := firstNonEmpty(*passwordFlag, os.Getenv("SUPERUSER_PASSWORD"))
	if password == "" {
		password, err = promptPassword()
		if err != nil {
			return err
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	repo := user.NewRepository(pool)
	u, created, err := createSuperuser(ctx, repo, email, password, *updatePassword)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("created superuser %s (%s)\n", u.Email, u.ID)
	} else {
		fmt.Printf("updated password for %s (%s)\n", u.Email, u.ID)
	}
	return nil
}

// errUserExists is returned when the email is already taken and the caller did
// not ask to update the password.
var errUserExists = errors.New("a user with that email already exists (use -update-password to reset it)")

// createSuperuser hashes the password and either creates a new superuser or,
// when updatePassword is set and the email already exists, resets that user's
// password. It never logs or returns the plaintext password. The bool result
// reports whether a new account was created (vs. an existing one updated).
func createSuperuser(ctx context.Context, repo *user.Repository, email, password string, updatePassword bool) (user.User, bool, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return user.User{}, false, err
	}

	u, err := repo.Create(ctx, email, hash, true)
	switch {
	case err == nil:
		return u, true, nil
	case errors.Is(err, user.ErrEmailTaken):
		if !updatePassword {
			return user.User{}, false, errUserExists
		}
		existing, gerr := repo.GetByEmail(ctx, email)
		if gerr != nil {
			return user.User{}, false, gerr
		}
		updated, uerr := repo.UpdatePassword(ctx, existing.ID, hash)
		if uerr != nil {
			return user.User{}, false, uerr
		}
		return updated, false, nil
	default:
		return user.User{}, false, err
	}
}

// parseEmail validates and normalizes an email address.
func parseEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("email is required (-email or SUPERUSER_EMAIL)")
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return "", fmt.Errorf("invalid email %q", raw)
	}
	return addr.Address, nil
}

// promptPassword reads a password from the terminal without echoing it. It
// requires an interactive terminal; in non-interactive contexts the password
// must come from a flag or env so it is never echoed into logs.
func promptPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("no password provided and stdin is not a terminal (set -password or SUPERUSER_PASSWORD)")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	return string(b), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
