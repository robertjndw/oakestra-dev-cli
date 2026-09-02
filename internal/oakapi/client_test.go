package oakapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientRelogsInOn401AndReplaysTheBody guards against the body-replay
// bug described in attempt's doc comment: a naive retry that reuses the
// first request's *bytes.Reader resends an empty body on the second
// attempt. This checks the retry sends the same body again, under the
// refreshed token.
func TestClientRelogsInOn401AndReplaysTheBody(t *testing.T) {
	var (
		postBodies []string
		loginCount int
		tokens     = []string{"first-token", "second-token"}
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		token := tokens[loginCount]
		loginCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
	})
	mux.HandleFunc("/api/application/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		postBodies = append(postBodies, string(body))

		if r.Header.Get("Authorization") == "Bearer first-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.Login("Admin", "Admin"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	const payload = `{"application_name":"e2e123"}`
	status, body, err := c.Post("/api/application/", []byte(payload))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (should have retried after the 401)", status)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %s, want the second attempt's response", body)
	}

	if len(postBodies) != 2 {
		t.Fatalf("server saw %d POSTs, want 2 (one 401, one retry)", len(postBodies))
	}
	for i, got := range postBodies {
		if got != payload {
			t.Errorf("POST %d body = %q, want %q (replay bug: reused reader sends an empty/stale body)", i, got, payload)
		}
	}

	if loginCount != 2 {
		t.Errorf("login called %d times, want 2 (once up front, once after the 401)", loginCount)
	}
}

// A server that always 401s must not loop: the client re-logs in and
// retries exactly once, then surfaces the failure. And with no credentials
// stored at all, there is nothing to re-login with - the 401 passes straight
// through on the first attempt.
func TestClientDoesNotLoopOn401(t *testing.T) {
	t.Run("with credentials, exactly two attempts", func(t *testing.T) {
		attempts := 0
		mux := http.NewServeMux()
		mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
		})
		mux.HandleFunc("/api/clusters/active", func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.WriteHeader(http.StatusUnauthorized)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		c := NewClient(srv.URL)
		if err := c.Login("Admin", "Admin"); err != nil {
			t.Fatalf("Login: %v", err)
		}

		status, _, err := c.Get("/api/clusters/active")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if status != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 to surface after the retry also fails", status)
		}
		if attempts != 2 {
			t.Errorf("server saw %d requests, want exactly 2 (no infinite retry loop)", attempts)
		}
	})

	t.Run("without credentials, no retry at all", func(t *testing.T) {
		attempts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		c := NewClient(srv.URL) // never logged in
		status, _, err := c.Get("/api/clusters/active")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if status != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", status)
		}
		if attempts != 1 {
			t.Errorf("server saw %d requests, want exactly 1 (nothing to re-login with)", attempts)
		}
	})
}

func TestDecodeUnwrapsDoubleEncodedBodies(t *testing.T) {
	type doc struct {
		Name string `json:"name"`
	}

	tests := []struct {
		name    string
		raw     string
		want    doc
		wantErr bool
	}{
		{name: "plain object", raw: `{"name":"a"}`, want: doc{Name: "a"}},
		{name: "leading whitespace before object", raw: "  \n\t{\"name\":\"a\"}", want: doc{Name: "a"}},
		{name: "double-encoded object", raw: `"{\"name\":\"a\"}"`, want: doc{Name: "a"}},
		{name: "leading whitespace before double-encoded string", raw: "  \"{\\\"name\\\":\\\"a\\\"}\"", want: doc{Name: "a"}},
		{name: "malformed", raw: `{not json`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got doc
			err := Decode([]byte(tt.raw), &got)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Decode(%s) = nil error, want one", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode(%s): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("Decode(%s) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}

	t.Run("plain array", func(t *testing.T) {
		var got []doc
		if err := Decode([]byte(`[{"name":"a"},{"name":"b"}]`), &got); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
			t.Errorf("Decode(array) = %+v", got)
		}
	})
}

func TestProbeReturnsStatusWithoutAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Probe sent an Authorization header, want none (it's the unauthenticated health check)")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	status, err := Probe(srv.URL, 0)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if status != http.StatusNoContent {
		t.Errorf("status = %d, want 204", status)
	}
}
