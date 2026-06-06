// Command migrate is the database migration CLI for the gateway.
//
// Usage:
//
//	migrate up                 apply all pending migrations
//	migrate down [n]           roll back the most recent n migrations (default 1)
//	migrate status             list every migration and whether it is applied
//	migrate version            print the current (highest applied) version
//	migrate create <name>      scaffold a new timestamped up/down pair
//
// up/down/status/version load the same configuration the server uses (so
// DATABASE_URL and the other required env vars must be set, e.g. via .env);
// create needs no database. Run it from the backend directory (or the
// container) so new files land in ./migrations.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/db"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/db/migrate"
	"github.com/lexbryan/ai.it-dab.com/backend/migrations"
)

const migrationsDir = "migrations"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch cmd := os.Args[1]; cmd {
	case "create":
		runCreate(os.Args[2:])
	case "up", "down", "status", "version":
		runDB(cmd, os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "migrate: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func runCreate(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: migrate create <name>")
		os.Exit(2)
	}
	up, down, err := migrate.Create(migrationsDir, strings.Join(args, " "))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("created %s\ncreated %s\n", up, down)
}

func runDB(cmd string, args []string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := db.New(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	migs, err := migrate.Load(migrations.FS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "up":
		done, err := migrate.Up(ctx, pool, migs)
		exitOn(err)
		if len(done) == 0 {
			fmt.Println("already up to date")
			return
		}
		for _, v := range done {
			fmt.Printf("applied %s\n", v)
		}
	case "down":
		steps := 1
		if len(args) > 0 {
			n, err := strconv.Atoi(args[0])
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "migrate: down expects a positive integer, got %q\n", args[0])
				os.Exit(2)
			}
			steps = n
		}
		reverted, err := migrate.Down(ctx, pool, migs, steps)
		exitOn(err)
		if len(reverted) == 0 {
			fmt.Println("nothing to roll back")
			return
		}
		for _, v := range reverted {
			fmt.Printf("reverted %s\n", v)
		}
	case "status":
		st, err := migrate.GetStatus(ctx, pool, migs)
		exitOn(err)
		for _, s := range st {
			mark := "pending"
			if s.Applied {
				mark = "applied"
			}
			fmt.Printf("%-7s %s_%s\n", mark, s.Version, s.Name)
		}
	case "version":
		v, err := migrate.CurrentVersion(ctx, pool)
		exitOn(err)
		if v == "" {
			fmt.Println("(no migrations applied)")
			return
		}
		fmt.Println(v)
	}
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `migrate - database migration CLI

usage:
  migrate up                 apply all pending migrations
  migrate down [n]           roll back the most recent n migrations (default 1)
  migrate status             list every migration and whether it is applied
  migrate version            print the current (highest applied) version
  migrate create <name>      scaffold a new timestamped up/down pair
`)
}
