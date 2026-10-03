package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

const (
	a2aVersion       = "1.0"
	a2aMediaType     = "application/a2a+json"
	a2aRequestLimit  = 64 << 10
	a2aResponseLimit = 256 << 10
)

type a2aConnection struct {
	URL               string
	Token             string
	Tenant            string
	Timeout           time.Duration
	AllowInsecureHTTP bool
}

// a2aClient implements the SendMessage and GetTask HTTP+JSON bindings in
// A2A v1.0.1. It never follows agent-supplied URLs, retries a message, or cancels
// a remote task when its local context ends. The owner commits action intent
// before Send and must persist the returned task identity before polling.
type a2aClient struct {
	endpoint *url.URL
	token    string
	tenant   string
	http     *http.Client
}

func newA2AClient(config a2aConnection) (*a2aClient, error) {
	if err := (&manifest.InterventionTargetA2A{URL: config.URL, Token: config.Token, Tenant: config.Tenant,
		RequestTimeout: config.Timeout, AllowInsecureHTTP: config.AllowInsecureHTTP}).Validate(); err != nil {
		return nil, err
	}
	if err := validateTargetURL(config.URL); err != nil {
		return nil, errors.New("A2A endpoint is invalid or blocked")
	}
	u, _ := url.Parse(config.URL) // Validated without network I/O above.
	client := *effectHTTPClient(config.Timeout)
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &a2aClient{endpoint: u, token: config.Token, tenant: config.Tenant, http: &client}, nil
}

type a2aPart struct {
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// A2AReceipt retains correlation and protocol state only. Agent text, artifacts,
// status messages and metadata are untrusted and do not enter the action record.
type A2AReceipt struct {
	TaskID    string
	ContextID string
	MessageID string
	State     string
}

// Send uses a caller-supplied, durable message identity. A2A does not require
// servers to deduplicate it, so even a lost response is returned without replay.
func (c *a2aClient) Send(ctx context.Context, messageID string, parts []a2aPart) (A2AReceipt, error) {
	if !validA2AIdentity(messageID) || len(parts) == 0 || len(parts) > 4 {
		return A2AReceipt{}, errors.New("A2A message requires a bounded identity and one to four parts")
	}
	for _, part := range parts {
		if (part.Text == "") == (len(part.Data) == 0) || !utf8.ValidString(part.Text) || len(part.Text)+len(part.Data) > a2aRequestLimit {
			return A2AReceipt{}, errors.New("A2A message part is invalid or too large")
		}
		if len(part.Data) != 0 && (!utf8.Valid(part.Data) || validateA2AJSON(part.Data) != nil) {
			return A2AReceipt{}, errors.New("A2A data part is invalid")
		}
	}
	request := struct {
		Tenant  string `json:"tenant,omitempty"`
		Message struct {
			MessageID string    `json:"messageId"`
			Role      string    `json:"role"`
			Parts     []a2aPart `json:"parts"`
		} `json:"message"`
		Configuration struct {
			AcceptedOutputModes []string `json:"acceptedOutputModes"`
			HistoryLength       int      `json:"historyLength"`
			ReturnImmediately   bool     `json:"returnImmediately"`
		} `json:"configuration"`
	}{Tenant: c.tenant}
	request.Message.MessageID, request.Message.Role, request.Message.Parts = messageID, "ROLE_USER", parts
	request.Configuration.AcceptedOutputModes = []string{"text/plain", "application/json"}
	request.Configuration.ReturnImmediately = true
	body, err := json.Marshal(request)
	if err != nil || len(body) > a2aRequestLimit {
		return A2AReceipt{}, errors.New("A2A encoded message is invalid or too large")
	}
	data, err := c.request(ctx, http.MethodPost, c.path("message:send"), body)
	if err != nil {
		return A2AReceipt{}, err
	}
	object, err := a2aObject(data)
	if err != nil {
		return A2AReceipt{}, err
	}
	task, hasTask := object["task"]
	message, hasMessage := object["message"]
	_, hasError := object["error"]
	if hasTask == hasMessage || hasError {
		return A2AReceipt{}, errors.New("A2A response must contain exactly one task or message")
	}
	if hasTask {
		return decodeA2ATask(task)
	}
	return decodeA2AMessage(message)
}

// Task reads the original task only. A missing task or failed read is not
// permission to send the original action again.
func (c *a2aClient) Task(ctx context.Context, original A2AReceipt) (A2AReceipt, error) {
	if !validA2ATaskID(original.TaskID) || original.MessageID != "" || (original.ContextID != "" && !validA2AIdentity(original.ContextID)) {
		return A2AReceipt{}, errors.New("A2A task reference is invalid")
	}
	target := c.path("tasks/" + url.PathEscape(original.TaskID))
	query := url.Values{"historyLength": {"0"}}
	if c.tenant != "" {
		query.Set("tenant", c.tenant)
	}
	target.RawQuery = query.Encode()
	data, err := c.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return A2AReceipt{}, err
	}
	receipt, err := decodeA2ATask(data)
	if err != nil {
		return A2AReceipt{}, err
	}
	if receipt.TaskID != original.TaskID || (original.ContextID != "" && receipt.ContextID != original.ContextID) {
		return A2AReceipt{}, errors.New("A2A task response has a different identity")
	}
	return receipt, nil
}

func (c *a2aClient) path(suffix string) *url.URL {
	u := *c.endpoint
	u.RawPath = strings.TrimRight(u.EscapedPath(), "/") + "/" + suffix
	u.Path, _ = url.PathUnescape(u.RawPath)
	return &u
}

// a2aHTTPError records an HTTP response, not proof that an action was not
// executed. Its status must not opt a mutation into automatic retry.
type a2aHTTPError struct{ Status int }

func (e *a2aHTTPError) Error() string { return fmt.Sprintf("A2A returned HTTP %d", e.Status) }

func (c *a2aClient) request(ctx context.Context, method string, target *url.URL, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("A2A request is invalid")
	}
	req.Header.Set("Accept", a2aMediaType+", application/json")
	req.Header.Set("A2A-Version", a2aVersion)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", a2aMediaType)
		// Neither Go nor an intermediary may infer a replay contract from this
		// body. The protocol's messageId is correlation, not an idempotency key.
		req.GetBody = nil
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, a2aNetworkError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &a2aHTTPError{Status: resp.StatusCode}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mediaType != a2aMediaType && mediaType != "application/json") {
		return nil, errors.New("A2A response has an unsupported content type")
	}
	if resp.ContentLength > a2aResponseLimit {
		return nil, errors.New("A2A response exceeds the size limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, a2aResponseLimit+1))
	if err != nil {
		return nil, a2aNetworkError(err)
	}
	if len(data) > a2aResponseLimit {
		return nil, errors.New("A2A response exceeds the size limit")
	}
	if !utf8.Valid(data) || validateA2AJSON(data) != nil {
		return nil, errors.New("A2A response contains invalid or ambiguous JSON")
	}
	return data, nil
}

func a2aNetworkError(err error) error {
	// Transport errors can contain the configured endpoint, proxy credentials,
	// certificate names, or peer-controlled text. Retain only cancellation.
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("A2A request interrupted; remote outcome may be unknown: %w", context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("A2A request deadline exceeded; remote outcome may be unknown: %w", context.DeadlineExceeded)
	}
	return errors.New("A2A request failed; remote outcome may be unknown")
}

func validA2AIdentity(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validA2ATaskID(value string) bool {
	// A2A reserves colon suffixes for task operations such as :subscribe.
	return validA2AIdentity(value) && value != "." && value != ".." && !strings.ContainsAny(value, "/\\%:")
}

func a2aObject(data []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, errors.New("A2A response requires a JSON object")
	}
	return object, nil
}

func a2aString(object map[string]json.RawMessage, name, protoName string, required bool) (string, error) {
	value, exists := object[name]
	if alias, hasAlias := object[protoName]; protoName != "" && hasAlias {
		if exists {
			return "", errors.New("A2A response contains duplicate field spellings")
		}
		value, exists = alias, true
	}
	if !exists && !required {
		return "", nil
	}
	var text string
	if json.Unmarshal(value, &text) != nil || ((required || text != "") && !validA2AIdentity(text)) {
		return "", errors.New("A2A response has an invalid identity or state")
	}
	return text, nil
}

func decodeA2ATask(data []byte) (A2AReceipt, error) {
	object, err := a2aObject(data)
	if err != nil {
		return A2AReceipt{}, err
	}
	id, err := a2aString(object, "id", "", true)
	if err != nil || !validA2ATaskID(id) {
		return A2AReceipt{}, errors.New("A2A response has an invalid task identity")
	}
	contextID, err := a2aString(object, "contextId", "context_id", false)
	if err != nil {
		return A2AReceipt{}, err
	}
	status, err := a2aObject(object["status"])
	if err != nil {
		return A2AReceipt{}, err
	}
	state, err := a2aString(status, "state", "", true)
	if err != nil {
		return A2AReceipt{}, err
	}
	// Unknown enum names remain observations; the action layer must not infer
	// success from a future state that this binary does not understand.
	return A2AReceipt{TaskID: id, ContextID: contextID, State: state}, nil
}

func decodeA2AMessage(data []byte) (A2AReceipt, error) {
	object, err := a2aObject(data)
	if err != nil {
		return A2AReceipt{}, err
	}
	id, err := a2aString(object, "messageId", "message_id", true)
	if err != nil {
		return A2AReceipt{}, err
	}
	role, err := a2aString(object, "role", "", true)
	if err != nil || role != "ROLE_AGENT" {
		return A2AReceipt{}, errors.New("A2A response message must have the agent role")
	}
	contextID, err := a2aString(object, "contextId", "context_id", false)
	if err != nil {
		return A2AReceipt{}, err
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(object["parts"], &parts) != nil || len(parts) == 0 {
		return A2AReceipt{}, errors.New("A2A response message has no valid parts")
	}
	for _, part := range parts {
		count := 0
		for _, key := range []string{"text", "raw", "url", "data"} {
			if value, exists := part[key]; exists {
				count++
				if key != "data" {
					var text string
					if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &text) != nil {
						return A2AReceipt{}, errors.New("A2A response part requires a string")
					}
				}
			}
		}
		if count != 1 {
			return A2AReceipt{}, errors.New("A2A response message has an invalid part")
		}
	}
	return A2AReceipt{MessageID: id, ContextID: contextID}, nil
}

// ProtoJSON accepts snake_case aliases, but duplicate fields are ambiguous.
// Check all nested values before extracting the small receipt; bounded depth
// also applies to ignored artifacts and metadata.
func validateA2AJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("A2A JSON nesting limit exceeded")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		if delim != '{' && delim != '[' {
			return errors.New("A2A JSON delimiter is invalid")
		}
		keys := make(map[string]bool)
		for decoder.More() {
			if delim == '{' {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok {
					return errors.New("A2A JSON object key is invalid")
				}
				if keys[key] {
					return errors.New("A2A JSON contains duplicate fields")
				}
				keys[key] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
			return errors.New("A2A JSON closing delimiter is invalid")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("A2A JSON contains trailing data")
	}
	return nil
}
