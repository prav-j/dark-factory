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
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/authn"
	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/health"
	"github.com/prav-j/dark-factory/internal/registry"
	"github.com/prav-j/dark-factory/internal/runtoken"
)

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

	// Internal execution-plane gateway: session pods call these routes with
	// their run token to reach the model and tool gateways.
	checker := newSessionChecker()
	tokens := runtoken.New([]byte(runTokenSecret()), checker, nil)
	internal := &registry.InternalGateway{
		Tokens: tokens,
		Model:  registry.NewScriptedCompleterFromEnv(),
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
