package httpserver

import (
	"net/http"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

// TestNew_NoTruncatingWriteTimeout is the streaming-safety guard: a global
// WriteTimeout would cut long-lived SSE responses, so it must stay zero.
func TestNew_NoTruncatingWriteTimeout(t *testing.T) {
	srv := New(config.Config{ListenAddr: ":9999"}, http.NewServeMux())

	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (a global write timeout truncates SSE)", srv.WriteTimeout)
	}
	if srv.Addr != ":9999" {
		t.Errorf("Addr = %q, want :9999", srv.Addr)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout should be set (slowloris protection)")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout should be set")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout should be set")
	}
	if srv.Handler == nil {
		t.Error("Handler should be set")
	}
}

func TestNew_DefaultsListenAddr(t *testing.T) {
	srv := New(config.Config{}, nil)
	if srv.Addr != ":8080" {
		t.Errorf("Addr = %q, want default :8080", srv.Addr)
	}
}
