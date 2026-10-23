package auth

// Signing-algorithm tests for the real go-oidc verifier built by
// NewKeycloakClient (platform#191, analog backend#99): a local OIDC provider
// (discovery + JWKS from httptest) advertises the same id_token algorithms as
// Keycloak 26; the JWKS holds an RSA key and an EC key, so RS512/PS256/ES256
// tokens are valid apart from the algorithm. No live Keycloak is needed.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	kcTestKid   = "test-kid"
	kcTestKidEC = "test-kid-ec"
	kcTestAud   = "at.ourproject.vfeeg.app"
)

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// newTestProvider starts a fake OIDC provider and returns its issuer URL.
func newTestProvider(t *testing.T, rsaKey *rsa.PrivateKey, ecKey *ecdsa.PrivateKey) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	issuer := srv.URL + "/realms/test"
	mux.HandleFunc("/realms/test/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"jwks_uri":               issuer + "/protocol/openid-connect/certs",
			"token_endpoint":         issuer + "/protocol/openid-connect/token",
			"authorization_endpoint": issuer + "/protocol/openid-connect/auth",
			// as Keycloak 26 advertises them
			"id_token_signing_alg_values_supported": []string{
				"PS384", "RS384", "EdDSA", "ES384", "HS256", "HS512", "ES256", "RS256",
				"HS384", "ES512", "PS256", "PS512", "RS512",
			},
		})
	})
	mux.HandleFunc("/realms/test/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		pub := rsaKey.PublicKey
		ec := ecKey.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": kcTestKid, "kty": "RSA", "alg": "RS256", "use": "sig",
			"n": b64url(pub.N.Bytes()),
			"e": b64url(big.NewInt(int64(pub.E)).Bytes()),
		}, {
			"kid": kcTestKidEC, "kty": "EC", "alg": "ES256", "use": "sig", "crv": "P-256",
			"x": b64url(ec.X.FillBytes(make([]byte, 32))),
			"y": b64url(ec.Y.FillBytes(make([]byte, 32))),
		}}})
	})
	return issuer
}

// signToken builds a compact JWS with alg; the kid follows the key type.
func signToken(t *testing.T, alg string, key crypto.Signer, issuer string) string {
	t.Helper()
	kid := kcTestKid
	if _, ok := key.(*ecdsa.PrivateKey); ok {
		kid = kcTestKidEC
	}
	h, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": kid})
	now := time.Now()
	p, _ := json.Marshal(map[string]any{
		"iss": issuer, "aud": kcTestAud, "sub": "user-1",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"tenant": []string{"RC100001"},
	})
	input := b64url(h) + "." + b64url(p)
	d256 := sha256.Sum256([]byte(input))
	var sig []byte
	var err error
	switch alg {
	case "RS256":
		sig, err = key.Sign(rand.Reader, d256[:], crypto.SHA256)
	case "RS512":
		d := sha512.Sum512([]byte(input))
		sig, err = key.Sign(rand.Reader, d[:], crypto.SHA512)
	case "PS256":
		sig, err = key.Sign(rand.Reader, d256[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256})
	case "ES256":
		// JWS wants r||s, not the ASN.1 form of crypto.Signer.
		var r, s *big.Int
		r, s, err = ecdsa.Sign(rand.Reader, key.(*ecdsa.PrivateKey), d256[:])
		if err == nil {
			sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
		}
	default:
		t.Fatalf("unsupported alg %s", alg)
	}
	if err != nil {
		t.Fatalf("sign %s: %v", alg, err)
	}
	return input + "." + b64url(sig)
}

// Only RS256 is accepted (CLAUDE.md, Authentication), in both verifier
// variants of NewKeycloakClient (with audience, and SkipClientIDCheck without
// one), although the discovery advertises more algorithms. The error must be
// go-jose's algorithm check, so a broken fixture cannot make a case pass.
func TestNewKeycloakClient_OnlyRS256Accepted(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := newTestProvider(t, rsaKey, ecKey)
	ctx := context.Background()

	variants := map[string]string{"with audience": kcTestAud, "SkipClientIDCheck": ""}
	cases := []struct {
		alg    string
		key    crypto.Signer
		accept bool
	}{
		{"RS256", rsaKey, true},
		{"RS512", rsaKey, false},
		{"PS256", rsaKey, false},
		{"ES256", ecKey, false},
	}
	const algErr = "unexpected signature algorithm"
	for name, audience := range variants {
		kc, err := NewKeycloakClient(ctx, issuer, kcTestAud, "", audience, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, tc := range cases {
			t.Run(name+"/"+tc.alg, func(t *testing.T) {
				_, err := kc.Verify(ctx, signToken(t, tc.alg, tc.key, issuer))
				if tc.accept {
					if err != nil {
						t.Fatalf("expected accepted, got %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), algErr) {
					t.Fatalf("expected error containing %q, got %v", algErr, err)
				}
			})
		}
	}
}
