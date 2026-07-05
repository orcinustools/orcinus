package deploy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// VerifyRegistryLogin checks that (username, password) can authenticate to the
// registry using the Docker Registry v2 handshake (HTTP basic, or bearer-token
// via the advertised token service). Returns nil on success or a descriptive
// error. When insecure is true, TLS certificate verification is skipped.
func VerifyRegistryLogin(ctx context.Context, server, username, password string, insecure bool) error {
	base := registryBaseURL(server)
	client := &http.Client{Timeout: 15 * time.Second}
	if insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}

	// One probe with basic auth: registries that use basic auth answer 200; those
	// that use token auth answer 401 with a Bearer challenge.
	status, wwwAuth, err := registryProbe(ctx, client, base, "", username, password)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusOK:
		return nil
	case status != http.StatusUnauthorized:
		return fmt.Errorf("registry %s: unexpected response HTTP %d", base, status)
	}

	// 401 → inspect the challenge.
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(wwwAuth)), "bearer") {
		// Basic (or unspecified) auth and we already sent credentials → invalid.
		return fmt.Errorf("login to %s failed: invalid credentials", base)
	}
	token, err := fetchBearerToken(ctx, client, wwwAuth, username, password)
	if err != nil {
		return fmt.Errorf("login to %s failed: %w", base, err)
	}
	status, _, err = registryProbe(ctx, client, base, token, "", "")
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	return fmt.Errorf("login to %s failed: invalid credentials (HTTP %d)", base, status)
}

// registryProbe does GET {base}/v2/ with either a bearer token or basic auth.
func registryProbe(ctx context.Context, client *http.Client, base, bearer, user, pass string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", nil)
	if err != nil {
		return 0, "", err
	}
	switch {
	case bearer != "":
		req.Header.Set("Authorization", "Bearer "+bearer)
	case user != "":
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("cannot reach registry %s: %w", base, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, resp.Header.Get("Www-Authenticate"), nil
}

// fetchBearerToken requests a token from the service named in a Bearer challenge.
func fetchBearerToken(ctx context.Context, client *http.Client, challenge, user, pass string) (string, error) {
	realm, params := parseBearerChallenge(challenge)
	if realm == "" {
		return "", fmt.Errorf("registry advertised no token realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("bad token realm %q: %w", realm, err)
	}
	q := u.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("invalid credentials (token endpoint HTTP %d)", resp.StatusCode)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(body, &t)
	if t.Token != "" {
		return t.Token, nil
	}
	if t.AccessToken != "" {
		return t.AccessToken, nil
	}
	return "", fmt.Errorf("token endpoint returned no token")
}

// parseBearerChallenge parses `Bearer realm="…",service="…",scope="…"`.
func parseBearerChallenge(h string) (realm string, params map[string]string) {
	params = map[string]string{}
	h = strings.TrimSpace(h)
	if i := strings.IndexByte(h, ' '); i >= 0 {
		h = h[i+1:] // drop the "Bearer" scheme word
	}
	for _, part := range strings.Split(h, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		if k == "realm" {
			realm = v
		} else {
			params[k] = v
		}
	}
	return realm, params
}

// registryBaseURL normalizes a registry host to a scheme+host base URL, mapping
// the Docker Hub aliases to the real registry endpoint.
func registryBaseURL(server string) string {
	s := strings.TrimSpace(server)
	scheme := "https"
	if strings.HasPrefix(s, "http://") {
		scheme, s = "http", strings.TrimPrefix(s, "http://")
	}
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimSuffix(s, "/")
	switch s {
	case "docker.io", "index.docker.io", "registry.docker.io", "index.docker.io/v1":
		return "https://registry-1.docker.io"
	}
	return scheme + "://" + s
}
