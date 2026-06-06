// Package app assembles the gateway's full route tree over a database pool. It
// is the single place every domain router is registered, shared by the server
// entrypoint (cmd/server) and the end-to-end test, so both exercise the exact
// same middleware chain and wiring.
package app

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/admin"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/apikey"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/conversation"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/gateway"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/gatewaycore"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/httpserver"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/ratelimit"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/token"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/usage"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/user"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/version"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/vllm"
)

// BuildHandler assembles the complete route tree over the given pool and returns
// the fully-wrapped handler (base middleware chain included). It is the single
// place the full route tree is registered:
//
//   - GET /version, GET /healthz, GET /readyz — unauthenticated foundation routes.
//   - POST /api/admin/login — per-IP rate-limited admin login.
//   - /api/admin/keys… — admin API-key CRUD behind the session-JWT guard.
//   - POST /v1/gateway/chat — the public LLM gateway behind the two-key auth
//     middleware with a per-credential rate limiter nested inside it (so the
//     limiter buckets by the resolved api_key_id, which auth attaches first).
func BuildHandler(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool) http.Handler {
	router := httpserver.NewRouter(cfg, logger)

	httpserver.RegisterHealth(router, httpserver.HealthDeps{
		DB:        pool,
		VLLMProbe: httpserver.NewVLLMProbe(cfg.VLLMURL),
		Version:   version.String(),
	})

	users := user.NewRepository(pool)
	keys := apikey.NewRepository(pool)
	convs := conversation.NewRepository(pool)

	// Admin plane: session-JWT auth over the admin login + key CRUD.
	issuer := token.NewIssuer(cfg.JWTSecret, 0) // 0 -> token.DefaultTTL
	adminAuthn := admin.NewAuthenticator(issuer)
	secureCookies := strings.EqualFold(cfg.Env, "production")
	loginRL := ratelimit.PerIP(cfg.LoginRateLimit)
	router.Handle("POST /api/admin/login", loginRL(admin.NewLoginHandler(users, issuer, secureCookies)))
	admin.RegisterKeyRoutes(router, adminAuthn, admin.NewKeysHandler(keys))

	// Public gateway: two-key auth, then a per-credential rate limiter, then the
	// chat handler. Order matters — the limiter reads the api_key_id the auth
	// middleware resolves into the request context.
	core := gatewaycore.NewService(convs, 0) // 0 -> default history cap
	// The audit recorder is best-effort and async, so a slow or failing usage
	// write never adds latency to, or fails, a gateway call.
	auditor := usage.Async(usage.NewRepository(pool), logger)
	chat := gateway.NewChatHandler(core, vllm.New(cfg), auditor)
	gatewayAuthn := gateway.NewAuthenticator(keys)
	gatewayRL := ratelimit.PerKey(cfg.GatewayRateLimit, gatewayCredentialKey)
	router.Handle("POST "+gateway.GatewayChatPath,
		gatewayAuthn.RequireCredential(gatewayRL(http.HandlerFunc(chat.Chat))))

	return router.Handler()
}

// gatewayCredentialKey extracts the resolved credential id the two-key middleware
// attached, so the gateway rate limiter buckets per calling project rather than
// per IP. Before auth resolves a credential it returns "", which PerKey treats as
// a pass-through.
func gatewayCredentialKey(r *http.Request) string {
	if cred, ok := gateway.CredentialFromContext(r.Context()); ok {
		return cred.ID
	}
	return ""
}
