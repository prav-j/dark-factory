// Package credexchange hosts the git credential-helper logic used by the
// Environment Builder and session clones (specs/15): git invokes the helper,
// which exchanges the session's Run Token for repo credentials in memory.
package credexchange

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var client = &http.Client{Timeout: 10 * time.Second}

func newRequest(ctx context.Context, url, body string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// HelperConfig configures the git credential helper.
type HelperConfig struct {
	ExchangeURL   string // POST target for credential exchange
	RunToken      string // current session's run token
	CredentialRef string // secret ID holding the repo credential
}

// GitCredentialGet implements the "get" operation of the git credential
// protocol: reads key=value lines, returns username/password lines.
func GitCredentialGet(ctx context.Context, cfg HelperConfig, stdin io.Reader, stdout io.Writer) error {
	// Consume git's request (we key off configured ref, not the URL).
	scanner := bufio.NewScanner(stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
	}

	body := fmt.Sprintf(`{"runToken":%q,"credentialRef":%q}`, cfg.RunToken, cfg.CredentialRef)
	req, err := newRequest(ctx, cfg.ExchangeURL, body)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("credential exchange: status %d", resp.StatusCode)
	}

	var out struct {
		Username string `json:"username"`
		Secret   string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "username=%s\npassword=%s\n", out.Username, out.Secret)
	return nil
}
