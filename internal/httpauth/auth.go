// Package httpauth authenticates committed management principals.
// It stores token verifiers, never plaintext tokens. It performs no persistence,
// token provisioning, provider I/O, or durable revocation itself. The owner must
// install committed configuration and reset authentication during backup restore.
package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const (
	Reader                = "reader"
	Operator              = "operator"
	MaxPrincipals         = 1024
	MaxTokenBytes         = 512
	MaxAuthorizationBytes = 2048
	LegacyPrincipalID     = "legacy-read"
)

var (
	ErrUnauthorized  = errors.New("management authentication required")
	ErrForbidden     = errors.New("management operation forbidden")
	ErrInvalidConfig = errors.New("invalid management authentication configuration")
	ErrInvalidToken  = errors.New("invalid management token")
)

// Error classifies an authentication or authorization failure without retaining
// a request, credential, verifier or backend error. Use errors.Is or errors.As.
type Error struct{ forbidden bool }

func (e *Error) Error() string { return e.Unwrap().Error() }
func (e *Error) Unwrap() error {
	if e.forbidden {
		return ErrForbidden
	}
	return ErrUnauthorized
}
func (e *Error) StatusCode() int {
	if e.forbidden {
		return http.StatusForbidden
	}
	return http.StatusUnauthorized
}
func unauthorized() error { return &Error{} }
func forbidden() error    { return &Error{forbidden: true} }

// Principal binds an immutable identity to a verifier for a high-entropy bearer
// token. Generate at least 256 random bits outside this package. TokenSHA256 is a
// 64-character hex SHA-256 digest, not a password hash or a plaintext token.
// Verifiers are excluded from JSON diagnostics; runtime configuration uses YAML.
type Principal struct {
	ID          string    `yaml:"id" json:"id"`
	Role        string    `yaml:"role" json:"role"`
	TokenSHA256 string    `yaml:"token_sha256" json:"-"`
	Revoked     bool      `yaml:"revoked,omitempty" json:"revoked,omitempty"`
	ExpiresAt   time.Time `yaml:"expires_at,omitempty" json:"expiresAt,omitempty"`
}

// ProxyConfig opts into a TLS-terminating proxy on loopback. The proxy must remove
// client-supplied forwarding headers and set exactly X-Forwarded-Proto: https.
// PublicOrigin is a fixed HTTPS origin; forwarded host/client-address headers are
// never trusted. Configure host isolation separately if local users are untrusted.
type ProxyConfig struct {
	PublicOrigin string `yaml:"public_origin" json:"publicOrigin"`
}

// Config can be validated before opening durable storage. A file is a bootstrap
// policy only; the runtime installs committed authority through FromAuthentication.
type Config struct {
	allowEmpty        bool
	Principals        []Principal  `yaml:"principals" json:"principals"`
	LegacyTokenSHA256 string       `yaml:"legacy_token_sha256,omitempty" json:"-"`
	TrustedProxy      *ProxyConfig `yaml:"trusted_proxy,omitempty" json:"trustedProxy,omitempty"`
}

type principal struct {
	id, role  string
	digest    [32]byte
	revoked   bool
	expiresAt time.Time
}
type operation struct {
	method, path string
	mediaTypes   map[string]bool
	bodyRequired bool
}
type policy struct {
	principals  []principal
	legacy      *[32]byte
	proxyOrigin string
	operations  map[string]operation
	read, write []string
}

// Authorizer owns an immutable policy behind a read/write lock. Replace takes
// effect atomically. AccessInfo returned by Authorize is an observation, not a
// lasting grant. Use WithAdmission at the actual synchronous commit boundary.
type Authorizer struct {
	mu         sync.RWMutex
	current    *policy
	generation uint64
	now        func() time.Time
}

// HashToken computes a verifier without retaining token text. Length and syntax
// checks do not establish entropy: token material must be cryptographically random.
func HashToken(token string) (string, error) {
	if len(token) < 32 || !validToken(token) {
		return "", ErrInvalidToken
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:]), nil
}

// ValidateConfig checks bootstrap configuration and the build-selected contract.
func ValidateConfig(config Config) error { _, err := compile(config); return err }

// New freezes a validated configuration without opening storage or making requests.
func New(config Config) (*Authorizer, error) {
	p, err := compile(config)
	if err != nil {
		return nil, err
	}
	return &Authorizer{current: p, generation: 1, now: time.Now}, nil
}

// Replace validates and installs a policy atomically, revoking removed, replaced
// and marked-revoked tokens. It waits for already-admitted synchronous callbacks;
// callers must coordinate the corresponding durable authorization boundary.
func (a *Authorizer) Replace(config Config) error {
	if a == nil {
		return ErrInvalidConfig
	}
	next, err := compile(config)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.current = next
	a.generation++
	return nil
}

// Generation identifies the currently installed policy. It is process-local and
// cannot replace the catalog's durable authentication revision or restore epoch.
func (a *Authorizer) Generation() uint64 {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.generation
}

// Authorize validates exact operation ID, method, path, transport, origin and
// credentials. Role never authorizes a worker execution route. Returned slices
// are owned by the caller. Recheck at admission if work is delayed after this call.
func (a *Authorizer) Authorize(r *http.Request, operationID string) (api.AccessInfo, error) {
	if a == nil {
		return api.AccessInfo{}, unauthorized()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return authorize(a.current, r, operationID, a.now())
}

// WithAdmission holds the policy read lock through a synchronous admission/commit
// callback, preventing Replace from revoking an authorization halfway through that
// boundary. Never hold this callback across provider work, streaming or waiting for
// controller application. The callback must not call any Authorizer method.
func (a *Authorizer) WithAdmission(r *http.Request, operationID string, admit func(api.AccessInfo) error) error {
	if a == nil || admit == nil {
		return unauthorized()
	}
	return a.WithPolicyAdmission(r, operationID, func(access api.AccessInfo, _ uint64) error { return admit(access) })
}

// WithPolicyAdmission also supplies the exact locked policy generation, allowing
// read cursors to bind to the authorization used for that snapshot. Its callback
// has the same synchronous, non-reentrant restrictions as WithAdmission.
func (a *Authorizer) WithPolicyAdmission(r *http.Request, operationID string, admit func(api.AccessInfo, uint64) error) error {
	if a == nil || admit == nil {
		return unauthorized()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	access, err := authorize(a.current, r, operationID, a.now())
	if err != nil {
		return err
	}
	return admit(access, a.generation)
}

func compile(config Config) (*policy, error) {
	if len(config.Principals) > MaxPrincipals || len(config.Principals) == 0 && config.LegacyTokenSHA256 == "" && !config.allowEmpty {
		return nil, ErrInvalidConfig
	}
	p := &policy{operations: make(map[string]operation)}
	seenIDs := make(map[string]bool)
	seenDigests := make(map[[32]byte]bool)
	for i, entry := range config.Principals {
		if !validID(entry.ID) || entry.ID == LegacyPrincipalID || seenIDs[entry.ID] || (entry.Role != Reader && entry.Role != Operator) || (!entry.ExpiresAt.IsZero() && (entry.ExpiresAt.Year() < 1 || entry.ExpiresAt.Year() > 9999)) {
			return nil, fmt.Errorf("%w: invalid principal at index %d", ErrInvalidConfig, i)
		}
		digest, ok := decodeVerifier(entry.TokenSHA256)
		if !ok || seenDigests[digest] {
			return nil, fmt.Errorf("%w: invalid verifier at index %d", ErrInvalidConfig, i)
		}
		seenIDs[entry.ID] = true
		seenDigests[digest] = true
		p.principals = append(p.principals, principal{id: entry.ID, role: entry.Role, digest: digest, revoked: entry.Revoked, expiresAt: entry.ExpiresAt})
	}
	if config.LegacyTokenSHA256 != "" {
		digest, ok := decodeVerifier(config.LegacyTokenSHA256)
		if !ok || seenDigests[digest] {
			return nil, fmt.Errorf("%w: invalid legacy verifier", ErrInvalidConfig)
		}
		p.legacy = &digest
	}
	if config.TrustedProxy != nil {
		origin, ok := canonicalOrigin(config.TrustedProxy.PublicOrigin)
		if !ok {
			return nil, fmt.Errorf("%w: invalid proxy origin", ErrInvalidConfig)
		}
		p.proxyOrigin = origin
	}
	var schema struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			RequestBody *struct {
				Required bool                       `json:"required"`
				Content  map[string]json.RawMessage `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	if json.Unmarshal(api.Schema(), &schema) != nil {
		return nil, fmt.Errorf("%w: invalid operation contract", ErrInvalidConfig)
	}
	for path, methods := range schema.Paths {
		for method, wire := range methods {
			if wire.OperationID == "" {
				continue
			}
			// The execution protocol has separate scoped worker credentials. A management
			// operator can inspect workers and manage JobTypes, but cannot act as a worker.
			if strings.HasPrefix(path, "/api/v2/external-workers/") {
				continue
			}
			op := operation{method: strings.ToUpper(method), path: path, mediaTypes: make(map[string]bool)}
			if wire.RequestBody != nil {
				op.bodyRequired = wire.RequestBody.Required
				for media := range wire.RequestBody.Content {
					op.mediaTypes[media] = true
				}
			}
			if _, duplicate := p.operations[wire.OperationID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate operation contract", ErrInvalidConfig)
			}
			p.operations[wire.OperationID] = op
			p.write = append(p.write, wire.OperationID)
			if op.method == http.MethodGet || op.method == http.MethodHead {
				p.read = append(p.read, wire.OperationID)
			}
		}
	}
	slices.Sort(p.read)
	slices.Sort(p.write)
	return p, nil
}

func authorize(p *policy, r *http.Request, operationID string, now time.Time) (api.AccessInfo, error) {
	var access api.AccessInfo
	if p == nil || r == nil || r.URL == nil {
		return access, unauthorized()
	}
	op, ok := p.operations[operationID]
	if !ok || r.Method != op.method || len(r.URL.Path) > 8192 || !matchPath(op.path, r.URL.Path) {
		return access, forbidden()
	}
	expectedOrigin, ok := secureOrigin(p, r)
	if !ok || !validOrigin(r, expectedOrigin) {
		return access, forbidden()
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > MaxAuthorizationBytes {
		return access, unauthorized()
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok {
		return access, unauthorized()
	}
	basic := strings.EqualFold(scheme, "Basic")
	if basic {
		user, password, ok := r.BasicAuth()
		if !ok || user != "cpra" || !validToken(password) {
			return access, unauthorized()
		}
		token = password
	} else if !strings.EqualFold(scheme, "Bearer") || !validToken(token) {
		return access, unauthorized()
	}
	digest := sha256.Sum256([]byte(token))
	// Compare every fixed-size verifier; do not stop at the first matching row.
	for _, entry := range p.principals {
		matches := subtle.ConstantTimeCompare(digest[:], entry.digest[:])
		if matches == 1 && !entry.revoked && !basic && (entry.expiresAt.IsZero() || now.Before(entry.expiresAt)) {
			access.PrincipalID = entry.id
			access.Role = entry.role
		}
	}
	if p.legacy != nil && subtle.ConstantTimeCompare(digest[:], p.legacy[:]) == 1 {
		access.PrincipalID = LegacyPrincipalID
		access.Role = Reader
	}
	if access.PrincipalID == "" {
		return api.AccessInfo{}, unauthorized()
	}
	permissions := p.read
	if access.Role == Operator {
		permissions = p.write
	}
	if _, allowed := slices.BinarySearch(permissions, operationID); !allowed || basic && op.method != http.MethodGet && op.method != http.MethodHead {
		return api.AccessInfo{}, forbidden()
	}
	if op.method != http.MethodGet && op.method != http.MethodHead && !validContentType(r, op) {
		return api.AccessInfo{}, forbidden()
	}
	access.Permissions = slices.Clone(permissions)
	return access, nil
}

func secureOrigin(p *policy, r *http.Request) (string, bool) {
	if r.TLS != nil {
		return canonicalOrigin("https://" + r.Host)
	}
	if p.proxyOrigin == "" {
		return "", false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", false
	}
	peer := net.ParseIP(host)
	if peer == nil || !peer.IsLoopback() {
		return "", false
	}
	proto := r.Header.Values("X-Forwarded-Proto")
	if len(proto) != 1 || proto[0] != "https" {
		return "", false
	}
	return p.proxyOrigin, true
}
func validOrigin(r *http.Request, expected string) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 || len(origins[0]) > 2048 {
		return false
	}
	origin, ok := canonicalOrigin(origins[0])
	return ok && origin == expected
}
func canonicalOrigin(value string) (string, bool) {
	if len(value) > 2048 {
		return "", false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return "https://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), true
}
func validContentType(r *http.Request, op operation) bool {
	values := r.Header.Values("Content-Type")
	if len(values) == 0 {
		return !op.bodyRequired && r.ContentLength == 0 && len(r.TransferEncoding) == 0
	}
	if len(values) != 1 || len(values[0]) > 256 {
		return false
	}
	media, _, err := mime.ParseMediaType(values[0])
	return err == nil && op.mediaTypes[media]
}
func matchPath(pattern, path string) bool {
	expected, actual := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(expected) != len(actual) {
		return false
	}
	for i, part := range expected {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			if actual[i] == "" || actual[i] == "." || actual[i] == ".." {
				return false
			}
			continue
		}
		if part != actual[i] {
			return false
		}
	}
	return true
}
func decodeVerifier(value string) ([32]byte, bool) {
	var digest [32]byte
	if len(value) != 64 {
		return digest, false
	}
	n, err := hex.Decode(digest[:], []byte(value))
	return digest, n == 32 && err == nil
}
func validToken(value string) bool {
	if len(value) == 0 || len(value) > MaxTokenBytes {
		return false
	}
	for _, c := range []byte(value) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func validID(value string) bool {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}
