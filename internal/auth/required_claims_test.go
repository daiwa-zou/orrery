package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/daiwa-zou/orrery/internal/config"
)

// A refused login shows the reason on the login page, and that is all the
// person gets — they cannot read the server's logs. Every way a required
// claim could refuse produced the same sentence: "claim %q does not permit
// access".
//
// The type mismatch is why it matters. requiredClaims compares text, the way
// kube-apiserver's --oidc-required-claim does, so an operator writing
// `email_verified: "true"` against a boolean claim locked out the entire
// organisation and told each of them, individually, that their claim did not
// permit access.

func TestRequiredClaimSaysWhichWayItFailed(t *testing.T) {
	a, p, _ := newFakeAuthenticator(t, nil)
	idTok, err := a.verifier.Verify(context.Background(), p.mintIDToken(map[string]any{
		"hd":             "other.example",
		"email_verified": true,
	}))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		required map[string]string
		want     []string
		notWant  []string
	}{
		{
			name:     "a value that does not match is a verdict on the token",
			required: map[string]string{"hd": "example.com"},
			want:     []string{`"hd"`, "does not permit access"},
			// The value the token carried is nobody else's business.
			notWant: []string{"other.example"},
		},
		{
			name:     "a claim the token does not carry says so",
			required: map[string]string{"department": "platform"},
			want:     []string{`"department"`, "does not carry"},
		},
		{
			name:     "a claim that is not text is a configuration problem",
			required: map[string]string{"email_verified": "true"},
			want:     []string{`"email_verified"`, "bool", "configuration problem"},
			// And must not be described as a permission the person lacks.
			notWant: []string{"does not permit access"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := &Authenticator{cfg: config.OIDCConfig{RequiredClaims: tc.required}}
			_, err := gate.userFromToken(idTok)
			if err == nil {
				t.Fatal("the login was allowed")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(err.Error(), notWant) {
					t.Errorf("error = %q, must not contain %q", err, notWant)
				}
			}
		})
	}
}

func TestRequiredClaimStillAdmitsAMatch(t *testing.T) {
	a, p, _ := newFakeAuthenticator(t, nil)
	idTok, err := a.verifier.Verify(context.Background(),
		p.mintIDToken(map[string]any{"hd": "example.com"}))
	if err != nil {
		t.Fatal(err)
	}

	gate := &Authenticator{cfg: config.OIDCConfig{
		RequiredClaims: map[string]string{"hd": "example.com"},
	}}
	if _, err := gate.userFromToken(idTok); err != nil {
		t.Errorf("a matching required claim refused the login: %v", err)
	}
}
