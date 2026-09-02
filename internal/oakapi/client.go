package oakapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a thin wrapper around http.Client with a base URL and JWT
// handling, replacing tests/helpers.py's ApiClient. Construct with a
// service's base URL, log in once, then call Get/Post/Delete with paths
// relative to that base - the Authorization header and 401 re-login are
// handled for you.
type Client struct {
	baseURL string
	http    *http.Client
	token   string

	// user/pass/haveCreds back the 401 re-login: a request that comes back
	// unauthorized re-logs in and retries exactly once, but only once
	// credentials actually exist to log in with.
	user, pass string
	haveCreds  bool
}

// NewClient builds a Client for baseURL. It does not contact the server -
// call Login before making authenticated requests.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Login authenticates and stores the bearer token for later requests. Also
// remembers the credentials, so a 401 on any later request triggers a
// silent re-login rather than failing the caller outright - the access
// token expires after ~15 minutes, which a slow E2E run can exceed.
func (c *Client) Login(user, pass string) error {
	body, err := json.Marshal(map[string]string{"username": user, "password": pass})
	if err != nil {
		return err
	}

	status, raw, err := c.attempt(http.MethodPost, "/api/auth/login", body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("login failed: status %d: %s", status, raw)
	}

	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("login response: %w", err)
	}
	if parsed.Token == "" {
		return fmt.Errorf("login returned no token: %s", raw)
	}

	c.token = parsed.Token
	c.user, c.pass, c.haveCreds = user, pass, true
	return nil
}

// Get issues a GET to path (relative to the client's base URL).
func (c *Client) Get(path string) (status int, body []byte, err error) {
	return c.do(http.MethodGet, path, nil)
}

// Post issues a POST to path with the given raw body (typically
// json.Marshal output).
func (c *Client) Post(path string, body []byte) (status int, respBody []byte, err error) {
	return c.do(http.MethodPost, path, body)
}

// Delete issues a DELETE to path.
func (c *Client) Delete(path string) (status int, body []byte, err error) {
	return c.do(http.MethodDelete, path, nil)
}

// do sends one request and, on a 401 from a client that has logged in
// before, re-logs in and retries exactly once.
func (c *Client) do(method, path string, body []byte) (int, []byte, error) {
	status, respBody, err := c.attempt(method, path, body)
	if err != nil {
		return 0, nil, err
	}
	if status != http.StatusUnauthorized || !c.haveCreds {
		return status, respBody, nil
	}

	if err := c.Login(c.user, c.pass); err != nil {
		return status, respBody, fmt.Errorf("re-login after 401: %w", err)
	}
	return c.attempt(method, path, body)
}

// attempt sends a single request, building a fresh *http.Request (and a
// fresh body reader) every call.
//
// This is the Go-specific trap the Python port has to get right: a
// *bytes.Reader is consumed by its first Do, so retrying by reusing a
// built request would silently resend an empty body and turn an expired
// token into a confusing 400 minutes into a run. req.GetBody does not help
// either - net/http's transport only invokes it to replay a request across
// a redirect, never for a caller-initiated resend. Holding the marshalled
// []byte here and rebuilding the request from it on every attempt is what
// makes the retry safe.
func (c *Client) attempt(method, path string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// Probe issues an unauthenticated GET to url with the given timeout,
// returning only the status code. Used for the bare health checks in
// tests/test_01_health.py, which bypass ApiClient entirely and use their own
// short timeouts (5s/10s) rather than Client's 15s.
func Probe(url string, timeout time.Duration) (int, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// MarshalJSON encodes v as compact JSON, like json.Marshal, but with HTML
// escaping turned off. json.Marshal's default escapes '&', '<' and '>' to
// & etc. - harmless to a JSON parser, but it means the bytes sent over
// the wire (and compared in tests) don't match a plain literal like the
// "&&" in the network test's client command. Use this - not json.Marshal -
// for any SLA/microservice payload built in this package.
func MarshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encoder.Encode always appends a trailing newline; callers expect the
	// same compact, unterminated form json.Marshal produces.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Decode unmarshals raw into v, unwrapping a double-encoded body: several
// system_manager endpoints return json_util.dumps(...) through
// flask-smorest, which serializes the already-serialized string again, so
// the body is a JSON string containing JSON rather than the object/array
// itself. Ports tests/helpers.py's json_body, which detects this by
// unmarshalling once and checking whether the result is a string; peeking at
// the first non-whitespace byte for a `"` here gets the same answer without
// a speculative double parse.
func Decode(raw []byte, v any) error {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return json.Unmarshal(raw, v)
	}

	var inner string
	if err := json.Unmarshal(raw, &inner); err != nil {
		return fmt.Errorf("decoding double-encoded body: %w", err)
	}
	return json.Unmarshal([]byte(inner), v)
}
