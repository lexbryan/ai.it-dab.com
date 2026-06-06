// Command server is the entrypoint for the DAB AI gateway.
//
// This scaffold prints a version banner and serves a single GET /healthz
// endpoint. The production router, CORS, middleware stack, configuration,
// persistence, and the LLM gateway itself are layered in by later tickets.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/httpserver"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/version"
)

func main() {
	log.Printf("DAB AI gateway %s starting", version.String())

	addr := ":8080"
	if v := os.Getenv("PORT"); v != "" {
		addr = ":" + v
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: httpserver.NewMux(),
	}

	// Minimal signal-driven shutdown so the scaffold exits cleanly (no panic)
	// on Ctrl-C / SIGTERM. Full lifecycle wiring lives in a later ticket.
	shutdownDone := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Printf("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown error: %v", err)
		}
		close(shutdownDone)
	}()

	log.Printf("listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}

	<-shutdownDone
	log.Printf("shutdown complete")
}
