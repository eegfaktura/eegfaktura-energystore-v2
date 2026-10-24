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
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
	// kcOtherAud is an aud that is not the clientID: accepted only in the
	// SkipClientIDCheck variant.
	kcOtherAud = "account"
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

// signingInput builds header.payload of a token that is valid apart from
// the signature.
func signingInput(alg, kid, issuer, aud string) string {
	h, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": kid})
	now := time.Now()
	p, _ := json.Marshal(map[string]any{
		"iss": issuer, "aud": aud, "sub": "user-1",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"tenant": []string{"RC100001"},
	})
	return b64url(h) + "." + b64url(p)
}

// signToken builds a compact JWS with alg; the kid follows the key type.
func signToken(t *testing.T, alg string, key crypto.Signer, issuer, aud string) string {
	t.Helper()
	kid := kcTestKid
	if _, ok := key.(*ecdsa.PrivateKey); ok {
		kid = kcTestKidEC
	}
	input := signingInput(alg, kid, issuer, aud)
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

// signHS256 signs with HS256 and the given secret (key confusion: the
// realm's public RSA key as HMAC secret).
func signHS256(secret []byte, issuer, aud string) string {
	input := signingInput("HS256", kcTestKid, issuer, aud)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(input))
	return input + "." + b64url(m.Sum(nil))
}

// testVariants are the two verifier variants of NewKeycloakClient. tokenAud
// differs from the clientID in the SkipClientIDCheck variant, so an accepted
// RS256 token proves that branch.
var testVariants = []struct {
	name, audience, tokenAud string
}{
	{"with audience", kcTestAud, kcTestAud},
	{"SkipClientIDCheck", "", kcOtherAud},
}

// Only RS256 is accepted (CLAUDE.md, Authentication), in both verifier
// variants of NewKeycloakClient (with audience, and SkipClientIDCheck without
// one), although the discovery advertises more algorithms. The error must be
// go-jose's algorithm check, so a broken fixture cannot make a case pass.
// Trade-off: the match is on go-jose's error text, so a go-jose upgrade that
// rewords it fails this test (fix the constant then); it does not let a token
// through.
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
	for _, v := range testVariants {
		kc, err := NewKeycloakClient(ctx, issuer, kcTestAud, "", v.audience, nil)
		if err != nil {
			t.Fatalf("%s: %v", v.name, err)
		}
		for _, tc := range cases {
			t.Run(v.name+"/"+tc.alg, func(t *testing.T) {
				_, err := kc.Verify(ctx, signToken(t, tc.alg, tc.key, issuer, v.tokenAud))
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

// HS256 with the realm's public key as HMAC secret (key confusion) and
// alg=none are rejected in both variants. Only an error is asserted here:
// go-oidc/go-jose reject these at different stages (unsupported algorithm,
// no matching key, malformed JWS) and the message differs between versions.
func TestNewKeycloakClient_HS256AndNoneRejected(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := newTestProvider(t, rsaKey, ecKey)
	der, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	ctx := context.Background()
	for _, v := range testVariants {
		kc, err := NewKeycloakClient(ctx, issuer, kcTestAud, "", v.audience, nil)
		if err != nil {
			t.Fatalf("%s: %v", v.name, err)
		}
		tokens := map[string]string{
			"HS256 public key PEM": signHS256(pemKey, issuer, v.tokenAud),
			"HS256 public key DER": signHS256(der, issuer, v.tokenAud),
			"none":                 signingInput("none", kcTestKid, issuer, v.tokenAud) + ".",
		}
		for name, token := range tokens {
			t.Run(v.name+"/"+name, func(t *testing.T) {
				if _, err := kc.Verify(ctx, token); err == nil {
					t.Fatal("expected rejection, got nil error")
				}
			})
		}
	}
}
