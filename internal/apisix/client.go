// Package apisix is a read-focused client for the APISIX Admin API.
//
// The package owns every detail of the wire format — the response envelope,
// pagination, status-code meaning — and hands callers plain decoded resources.
// Nothing outside this package should know what an Admin API response looks
// like, so that a future APISIX response-format change is contained here.
//
// The wire behaviour implemented here was verified against
// apache/apisix:3.18.0-debian rather than inferred from documentation; the two
// disagree in places. See docs/compatibility.md.
package apisix

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// adminPrefix is the fixed Admin API path prefix.
const adminPrefix = "/apisix/admin"

// authHeader is the only credential channel SixSync uses.
//
// APISIX also accepts the key as an `api_key` query parameter and as a cookie.
// Both are avoided deliberately: a query parameter lands in access logs and
// proxy logs, which is exactly how an Admin API key leaks.
const authHeader = "X-API-KEY"

// maxResponseBytes caps how much of a response body is read. A compromised or
// misbehaving endpoint should not be able to exhaust the client's memory. 64
// MiB is far above any plausible Admin API list response while still bounding
// the damage.
const maxResponseBytes = 64 << 20

// defaultTimeout bounds a request when the caller sets none. An Admin API call
// against a large gateway is still a local network operation; ten seconds is
// generous without letting a hung endpoint stall a pipeline indefinitely.
const defaultTimeout = 10 * time.Second

// Options configures a Client.
type Options struct {
	// BaseURL is the Admin API root, e.g. https://gateway.example.test:9180.
	// Any /apisix/admin suffix is tolerated and normalized away.
	BaseURL string

	// APIKey is sent as X-API-KEY. May be empty, which is useful for probing
	// how an endpoint responds to an unauthenticated request.
	APIKey string

	// Timeout bounds each individual HTTP request.
	Timeout time.Duration

	// CAFile, if set, replaces the system trust store for verifying the
	// server. Used for a private CA.
	CAFile string

	// ClientCertFile and ClientKeyFile enable mTLS. APISIX treats a client
	// certificate as additional to the API key, never as a replacement.
	ClientCertFile string
	ClientKeyFile  string

	// InsecureSkipVerify disables server certificate verification.
	//
	// This makes the connection trivially interceptable, and the Admin API key
	// travels over it. It exists only because some users genuinely have a
	// throwaway self-signed lab endpoint. Callers must warn when it is set.
	InsecureSkipVerify bool

	// UserAgent identifies SixSync to the server and in access logs.
	UserAgent string

	// HTTPClient overrides transport construction entirely. Used by tests.
	HTTPClient *http.Client
}

// Client talks to one APISIX Admin API endpoint.
type Client struct {
	baseURL   string
	apiKey    string
	userAgent string
	timeout   time.Duration
	http      *http.Client
}

// New builds a Client, returning an error if the TLS material cannot be loaded.
func New(opts Options) (*Client, error) {
	base, err := normalizeBaseURL(opts.BaseURL)
	if err != nil {
		return nil, err
	}

	hc := opts.HTTPClient
	if hc == nil {
		tlsCfg, err := buildTLSConfig(opts)
		if err != nil {
			return nil, err
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = tlsCfg
		hc = &http.Client{Transport: transport, CheckRedirect: refuseRedirect}
	}

	// A per-request deadline is applied via context in do() rather than
	// through http.Client.Timeout, so that it covers reading the body as well
	// as establishing the connection, and so a caller's own shorter deadline
	// still wins.
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	ua := opts.UserAgent
	if ua == "" {
		ua = "sixsync"
	}

	return &Client{
		baseURL:   base,
		apiKey:    opts.APIKey,
		userAgent: ua,
		timeout:   timeout,
		http:      hc,
	}, nil
}

// refuseRedirect stops the client from following any redirect.
//
// Go strips well-known credential headers on a cross-host redirect, but
// X-API-KEY is a custom header and is not among them — so a redirect would
// send the Admin API key to whatever host the Location names. That could come
// from a compromised endpoint, a misconfigured proxy, or anyone able to inject
// a response on a plaintext connection.
//
// The Admin API does not redirect. One arriving is anomalous, and the useful
// response is to stop and say so rather than to follow it.
func refuseRedirect(req *http.Request, via []*http.Request) error {
	return fmt.Errorf("the endpoint returned a redirect to %s, which SixSync does not follow: "+
		"the Admin API does not redirect, and following one would send the API key to that "+
		"host. Point --admin-url at the Admin API directly", req.URL.Redacted())
}

// normalizeBaseURL validates the endpoint and strips any Admin API path
// suffix, so that a user pasting the URL they use with curl still works.
func normalizeBaseURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("admin API URL is empty")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("admin API URL is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("admin API URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("admin API URL has no host")
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), adminPrefix)
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

func buildTLSConfig(opts Options) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Verification is on unless the caller explicitly disables it. The
		// caller is responsible for warning loudly when it does.
		InsecureSkipVerify: opts.InsecureSkipVerify, //nolint:gosec // opt-in, documented, warned about
	}

	if opts.CAFile != "" {
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %s contains no valid PEM certificate", opts.CAFile)
		}
		cfg.RootCAs = pool
	}

	switch {
	case opts.ClientCertFile != "" && opts.ClientKeyFile != "":
		cert, err := tls.LoadX509KeyPair(opts.ClientCertFile, opts.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	case opts.ClientCertFile != "" || opts.ClientKeyFile != "":
		return nil, errors.New("client certificate and client key must be provided together")
	}

	return cfg, nil
}

// BaseURL returns the normalized endpoint, for display.
func (c *Client) BaseURL() string { return c.baseURL }

// rawResponse is one completed HTTP exchange.
type rawResponse struct {
	status  int
	headers http.Header
	body    []byte
}

// do performs one request and classifies any failure. It never logs the
// request body: for a mutation that body could hold credentials.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte) (*rawResponse, error) {
	// Bound this request. A caller's own deadline, if it is shorter, still
	// takes effect: WithTimeout never extends an existing one.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	u := c.baseURL + adminPrefix + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, &Error{Kind: ErrTransport, Method: method, Path: path, err: err}
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set(authHeader, c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Kind: ErrTransport, Method: method, Path: path, err: err}
	}
	defer resp.Body.Close() //nolint:errcheck // read-side close

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, &Error{Kind: ErrTransport, Method: method, Path: path, err: err}
	}

	out := &rawResponse{status: resp.StatusCode, headers: resp.Header, body: raw}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return out, nil
	}
	return out, classify(method, path, out)
}

// apiError is the error shape APISIX returns. `description` is only populated
// for authentication failures, and is the sole way to tell them apart.
type apiError struct {
	ErrorMsg    string `json:"error_msg"`
	Description string `json:"description"`
	Message     string `json:"message"`
}

func classify(method, path string, r *rawResponse) error {
	var ae apiError
	_ = json.Unmarshal(r.body, &ae) // a non-JSON error body is tolerated

	msg := ae.ErrorMsg
	if msg == "" {
		msg = ae.Message
	}

	e := &Error{StatusCode: r.status, Method: method, Path: path, Message: msg}

	switch r.status {
	case http.StatusUnauthorized:
		// All three auth outcomes are 401; only `description` separates them.
		switch {
		case strings.Contains(ae.Description, "missing apikey"):
			e.Kind = ErrAuthMissing
		case strings.Contains(ae.Description, "invalid method for role viewer"):
			e.Kind = ErrAuthInsufficient
		default:
			e.Kind = ErrAuthInvalid
		}
		if ae.Description != "" {
			e.Message = ae.Description
		}

	case http.StatusNotFound:
		e.Kind = ErrNotFound

	case http.StatusBadRequest:
		// A refused delete and a schema rejection are both 400; the message is
		// the only discriminator APISIX offers.
		if strings.Contains(msg, "still using it now") {
			e.Kind = ErrReferenced
		} else {
			e.Kind = ErrValidation
		}

	case http.StatusServiceUnavailable:
		// APISIX reports a lost internal compare-and-swap as 503. Treating
		// every 503 as a conflict would mislabel a real outage, so the message
		// decides.
		if strings.Contains(msg, "value changed before overwritten") {
			e.Kind = ErrConflict
		} else {
			e.Kind = ErrServer
		}

	default:
		e.Kind = ErrServer
	}

	return e
}

// ServerVersion reports the APISIX version advertised in the Server response
// header, e.g. "3.18.0".
//
// This is best-effort by nature: a reverse proxy may strip or rewrite Server.
// An empty string means "could not determine", never "not APISIX".
func ServerVersion(h http.Header) string {
	v := h.Get("Server")
	if _, rest, found := strings.Cut(v, "APISIX/"); found {
		return strings.TrimSpace(rest)
	}
	return ""
}
