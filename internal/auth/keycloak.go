package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// KeycloakClient wraps a Keycloak realm: OIDC discovery, JWT verifier, and
// password-grant authentication for ProtectAPI's Basic-Auth flow.
type KeycloakClient struct {
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	clientID     string
	clientSecret string
	http         *http.Client
}

// NewKeycloakClient performs OIDC discovery against `issuer` and builds the
// verifier.
//
// `audience` controls the JWT `aud` check: when non-empty, tokens must
// carry it in their audience claim. When empty, SkipClientIDCheck mirrors
// v1 — the audience claim of a platform JWT may name a different client,
// and the realm is the trust boundary. Set ESV2_AUTH_APP_AUDIENCE /
// ESV2_AUTH_API_AUDIENCE to enforce (audit 2026-06-12).
func NewKeycloakClient(ctx context.Context, issuer, clientID, clientSecret, audience string, httpClient *http.Client) (*KeycloakClient, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, httpClient), issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc discovery %s: %w", issuer, err)
	}
	// RS256 only (CLAUDE.md, Authentication): without SupportedSigningAlgs
	// go-oidc accepts every algorithm the discovery advertises except HS*/none.
	rs256 := []string{oidc.RS256}
	oidcCfg := &oidc.Config{ClientID: audience, SupportedSigningAlgs: rs256}
	if audience == "" {
		oidcCfg = &oidc.Config{ClientID: clientID, SkipClientIDCheck: true, SupportedSigningAlgs: rs256}
	}
	return &KeycloakClient{
		provider:     p,
		verifier:     p.Verifier(oidcCfg),
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         httpClient,
	}, nil
}

// Verify validates a raw bearer JWT.
func (k *KeycloakClient) Verify(ctx context.Context, raw string) (*oidc.IDToken, error) {
	return k.verifier.Verify(ctx, raw)
}

// idTokenResponse holds the relevant fields from a token endpoint reply.
type idTokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// AuthenticateUserWithPassword runs the password grant against Keycloak and
// returns the verified id_token. v1 used the same flow for ProtectAPI's
// Basic-Auth bridge.
func (k *KeycloakClient) AuthenticateUserWithPassword(ctx context.Context, username, password string) (*oidc.IDToken, error) {
	tokenURL := k.provider.Endpoint().TokenURL
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {k.clientID},
		"client_secret": {k.clientSecret},
		"scope":         {"openid"},
		"username":      {username},
		"password":      {password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("auth: password grant request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: password grant: %w", err)
	}
	defer resp.Body.Close()

	var body idTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("auth: password grant decode: %w", err)
	}
	if body.Error != "" {
		return nil, fmt.Errorf("auth: password grant: %s: %s", body.Error, body.ErrorDesc)
	}
	if body.IDToken == "" {
		return nil, fmt.Errorf("auth: password grant: empty id_token")
	}
	return k.Verify(ctx, body.IDToken)
}
