// Command git-credential-darkfactory is a git credential helper that
// exchanges the session's Run Token for repo credentials via the platform's
// credential exchange endpoint. Credentials live only in memory.
//
// Environment:
//
//	DARKFACTORY_EXCHANGE_URL  exchange endpoint URL
//	DARKFACTORY_RUN_TOKEN     current run token
//	DARKFACTORY_CREDENTIAL_REF secret ID holding the repo credential
package main

import (
	"context"
	"log"
	"os"

	"github.com/prav-j/dark-factory/internal/credexchange"
)

func main() {
	cfg := credexchange.HelperConfig{
		ExchangeURL:   os.Getenv("DARKFACTORY_EXCHANGE_URL"),
		RunToken:      os.Getenv("DARKFACTORY_RUN_TOKEN"),
		CredentialRef: os.Getenv("DARKFACTORY_CREDENTIAL_REF"),
	}

	op := "get"
	if len(os.Args) > 1 {
		op = os.Args[1]
	}
	switch op {
	case "get":
		if err := credexchange.GitCredentialGet(context.Background(), cfg, os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
	case "store", "erase":
		// No-op: we never persist credentials on disk.
	default:
		log.Fatalf("unknown operation %q", op)
	}
}
