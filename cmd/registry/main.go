// Command registry runs the Agent Registry control-plane service.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/authn"
	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/health"
	"github.com/prav-j/dark-factory/internal/policy"
	"github.com/prav-j/dark-factory/internal/registry"
	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/toolgw"
)

// orgPolicyPatterns holds tool-call globs configured via ORG_TOOL_PATTERNS.
var orgPolicyPatterns []string

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dsn := flag.String("database-url", os.Getenv("DATABASE_URL"), "postgres DSN")
	issuer := flag.String("oidc-issuer", os.Getenv("OIDC_ISSUER"), "OIDC issuer URL (e.g. http://localhost:8082)")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("DATABASE_URL (or -database-url) is required")
	}
	if *issuer == "" {
		log.Fatal("OIDC_ISSUER (or -oidc-issuer) is required")
	}

	if err := db.MigrateUp(*dsn); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	conn, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	if err := conn.Ping(); err != nil {
		log.Fatalf("ping db: %v", err)
	}
	defer func() { _ = conn.Close() }()

	mux := http.NewServeMux()
	h := health.NewHandler()
	h.RegisterRoutes(mux)
	h.Register("postgres", func() error { return conn.Ping() })

	auth := authn.NewAuthenticator(*issuer, &authn.DBResolver{DB: conn})
	mux.Handle("/v1/", auth.Middleware(registry.NewHTTPHandler(registry.NewStore(conn))))

	// Org tool-policy patterns for the local/live gateway (config-driven;
	// DB-backed policy storage lands with multi-org policy management).
	if raw := os.Getenv("ORG_TOOL_PATTERNS"); raw != "" {
		var pats []string
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				pats = append(pats, p)
			}
		}
		orgPolicyPatterns = pats
	}

	// Internal execution-plane gateway: session pods call these routes with
	// their run token to reach the model and tool gateways.
	checker := newSessionChecker()
	tokens := runtoken.New([]byte(runTokenSecret()), checker, nil)
	pol, err := policy.NewEngine()
	if err != nil {
		log.Fatalf("policy engine: %v", err)
	}
	if err := pol.SetOrgPatterns(orgPolicyPatterns); err != nil {
		log.Fatalf("org patterns: %v", err)
	}
	toolRegistry := toolgw.NewRegistry(1024 * 1024)
	toolRegistry.Register(toolgw.ToolDef{
		Name: "http_request", Description: "Allowlisted HTTP request",
		InputSchema:   `{"type":"object","required":["url"],"properties":{"url":{"type":"string"}}}`,
		RequiredScope: "net:fetch",
	}, toolgw.HTTPRequestTool([]string{"api.internal.corp", "example.com"}), nil, 1000)
	internal := &registry.InternalGateway{
		Tokens: tokens,
		Model:  registry.NewScriptedCompleterFromEnv(),
		Tools:  toolgw.New(toolRegistry, pol, tokens),
	}
	internal.Register(mux)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	done := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
		close(done)
	}()

	log.Printf("registry listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
	<-done
}
