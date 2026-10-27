package reconcile

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TrustedKey binds a key to both an issuer and one tenant. No token-directed fetches.
type TrustedKey struct {
	Issuer    string `json:"issuer"`
	Tenant    string `json:"tenant_id"`
	PublicKey string `json:"public_key"`
}
type Verifier struct {
	Keys          map[string]TrustedKey
	AllowFixtures bool
}

func strictDecode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("invalid JSON fields")
	}
	return nil
}

func (v Verifier) Accept(tenant string, raw []byte, now time.Time) (Decision, error) {
	var in DecisionInput
	if err := strictDecode(raw, &in); err != nil {
		return Decision{}, err
	}
	if (in.Fixture == nil) == (in.Token == "") {
		return Decision{}, fmt.Errorf("provide exactly one token or fixture")
	}
	verification := Verification{AcceptedAt: now.UTC()}
	var claims Claims
	if in.Fixture != nil {
		if !v.AllowFixtures {
			return Decision{}, fmt.Errorf("unsigned fixtures disabled")
		}
		claims = *in.Fixture
		verification.Mode = "unsigned-fixture"
	} else {
		parts := strings.Split(in.Token, ".")
		if len(parts) != 3 {
			return Decision{}, fmt.Errorf("invalid compact signature envelope")
		}
		decode := base64.RawURLEncoding.DecodeString
		headerRaw, err := decode(parts[0])
		if err != nil {
			return Decision{}, fmt.Errorf("invalid signature header")
		}
		var header struct {
			Algorithm string `json:"alg"`
			KeyID     string `json:"kid"`
			Type      string `json:"typ"`
		}
		if err = strictDecode(headerRaw, &header); err != nil || header.Algorithm != "EdDSA" || header.Type != "JWT" {
			return Decision{}, fmt.Errorf("unsupported signature header")
		}
		key, ok := v.Keys[header.KeyID]
		if !ok || key.Tenant != tenant {
			return Decision{}, fmt.Errorf("untrusted signing key")
		}
		public, err := decode(key.PublicKey)
		if err != nil || len(public) != ed25519.PublicKeySize {
			return Decision{}, fmt.Errorf("invalid configured signing key")
		}
		signature, err := decode(parts[2])
		if err != nil || !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), signature) {
			return Decision{}, fmt.Errorf("invalid decision signature")
		}
		payload, err := decode(parts[1])
		if err != nil {
			return Decision{}, fmt.Errorf("invalid claims encoding")
		}
		if err = strictDecode(payload, &claims); err != nil {
			return Decision{}, err
		}
		if claims.Issuer != key.Issuer {
			return Decision{}, fmt.Errorf("issuer mismatch")
		}
		verification.Mode = "ed25519"
		verification.KeyID = header.KeyID
		verification.SignatureValid = true
		verification.PublicKey = key.PublicKey
	}
	if err := ValidateClaims(claims, tenant, now); err != nil {
		return Decision{}, err
	}
	return Decision{ID: claims.ID, Claims: claims, Verification: verification, Original: json.RawMessage(append([]byte(nil), raw...))}, nil
}

// Sign is for the local demo/custom envelope, not Saifuro's production token schema.
func Sign(claims Claims, keyID string, key ed25519.PrivateKey) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": keyID, "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString
	body := enc(header) + "." + enc(payload)
	return body + "." + enc(ed25519.Sign(key, []byte(body))), nil
}
