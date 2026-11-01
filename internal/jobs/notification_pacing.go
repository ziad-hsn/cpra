package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type NotificationRateScope struct {
	ID       string
	Interval time.Duration
	Global   bool
	Serial   bool
}
type NotificationRateState struct {
	NotBefore    time.Time
	Spacing      time.Duration
	SpacingUntil time.Time
}
type NotificationRateUpdate struct {
	ID    string
	State NotificationRateState
}
type NotificationRateFeedback struct {
	At      time.Time
	Updates []NotificationRateUpdate
}

func rateIdentity(parts ...string) string {
	sum := sha256.Sum256([]byte("cpra-notification-limit-v1\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// ConfigureNotificationPacing receives a full private destination digest from
// preparation, never the queue's truncated flow key. Explicit scopes can group
// different integrations whose shared account/channel cannot be inferred.
func (d *Dispatch) ConfigureNotificationPacing(destination string, interval time.Duration, shared string) {
	if d.Kind != "code" || destination == "" {
		return
	}
	if d.Driver == "slack" {
		interval = max(interval, time.Second)
	}
	primary := rateIdentity(d.Driver, destination)
	global := ""
	serial := false
	job := d.Job
	for range 8 {
		switch c := job.(type) {
		case *CodeDiscordJob:
			if u, err := url.Parse(c.WebhookURL); err == nil {
				// Execute-webhook is one route. API-version and thread query
				// spellings do not create a new webhook major resource.
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				for i := 0; i+2 < len(parts); i++ {
					if parts[i] == "webhooks" {
						primary = rateIdentity("discord-webhook", rateOrigin(u), parts[i+1], parts[i+2])
						break
					}
				}
				global = rateIdentity("discord-egress", rateOrigin(u))
			}
			serial = true
		case *CodeOpsgenieJob:
			if target, err := opsgenieURL(c.URL); err == nil {
				if u, e := url.Parse(target); e == nil {
					primary = rateIdentity("opsgenie-alert-domain", rateOrigin(u), c.APIKey)
				}
			}
			// Keep the scope reserved while accepted asynchronous work is
			// polled. A response can impose an account pause before completion.
			// Distinct keys for one account require an explicit shared scope.
			serial = true
		case interface{ Unwrap() Job }:
			job = c.Unwrap()
			continue
		}
		break
	}
	d.RateScopes = []NotificationRateScope{{ID: primary, Interval: interval, Serial: serial}}
	if global != "" {
		d.RateScopes = append(d.RateScopes, NotificationRateScope{ID: global, Global: true})
	}
	if shared != "" {
		d.RateScopes = append(d.RateScopes, NotificationRateScope{ID: rateIdentity("operator", d.Driver, shared), Interval: interval, Serial: serial})
	}
}

func rateOrigin(u *url.URL) string {
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && !(scheme == "https" && port == "443" || scheme == "http" && port == "80") {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

type notificationRateKey struct{}
type notificationRateRecorder struct {
	mu       sync.Mutex
	driver   string
	scopes   []NotificationRateScope
	updates  map[string]NotificationRateState
	nextRead time.Time
}

func (d *Dispatch) rateRecorder() *notificationRateRecorder {
	if d.Kind != "code" || len(d.RateScopes) == 0 {
		return nil
	}
	return &notificationRateRecorder{driver: d.Driver, scopes: slices.Clone(d.RateScopes), updates: map[string]NotificationRateState{}}
}
func observeNotificationRate(ctx context.Context, response *http.Response) {
	r, _ := ctx.Value(notificationRateKey{}).(*notificationRateRecorder)
	if r == nil || response == nil {
		return
	}
	r.observe(response.Header, response.StatusCode, time.Now().UTC())
}
func rateHeader(h http.Header, name string) string {
	values := h.Values(name)
	if len(values) != 1 || len(values[0]) > 128 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
func rateSeconds(value string) (time.Duration, bool) {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 86400 {
		return 0, false
	}
	return time.Duration(math.Ceil(v * float64(time.Second))), true
}
func mergeRateState(old, next NotificationRateState) NotificationRateState {
	if next.NotBefore.After(old.NotBefore) {
		old.NotBefore = next.NotBefore
	}
	if next.SpacingUntil.After(old.SpacingUntil) {
		old.SpacingUntil = next.SpacingUntil
	}
	old.Spacing = max(old.Spacing, next.Spacing)
	return old
}
func (r *notificationRateRecorder) observe(h http.Header, status int, at time.Time) {
	state := NotificationRateState{}
	global := r.driver == "discord" && (rateHeader(h, "X-RateLimit-Global") == "true" || rateHeader(h, "X-RateLimit-Scope") == "global")
	if status == 429 || status >= 500 {
		if delay, ok := notificationRetryAfter(h, at); ok {
			state.NotBefore = at.Add(delay)
		}
	}
	if r.driver == "discord" {
		reset, ok := rateSeconds(rateHeader(h, "X-RateLimit-Reset-After"))
		remaining, err := strconv.ParseInt(rateHeader(h, "X-RateLimit-Remaining"), 10, 32)
		if ok && err == nil && remaining >= 0 && remaining <= 1_000_000 && reset > 0 {
			if remaining == 0 {
				state.NotBefore = maxTime(state.NotBefore, at.Add(reset))
			} else {
				state.Spacing = time.Duration((int64(reset) + remaining - 1) / remaining)
				state.SpacingUntil = at.Add(reset)
				state.NotBefore = maxTime(state.NotBefore, at.Add(state.Spacing))
			}
		}
	}
	if r.driver == "opsgenie" {
		stateName := rateHeader(h, "X-RateLimit-State")
		period, ok := rateSeconds(rateHeader(h, "X-RateLimit-Period-In-Sec"))
		if (stateName == "BURST" || stateName == "THROTTLED") && ok && (period == time.Second || period == time.Minute) {
			state.NotBefore = maxTime(state.NotBefore, at.Add(period))
		}
	}
	r.record(state, global, at)
}

func (r *notificationRateRecorder) record(state NotificationRateState, global bool, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, scope := range r.scopes {
		if scope.Global && !global {
			continue
		}
		if !state.NotBefore.IsZero() {
			r.updates[scope.ID] = mergeRateState(r.updates[scope.ID], state)
		}
		r.nextRead = maxTime(r.nextRead, at.Add(scope.Interval))
	}
	r.nextRead = maxTime(r.nextRead, state.NotBefore)
}

// The caller has decoded a bounded, duplicate-checked provider response. Body
// metadata supplements pacing only; the effect classifier owns retry permission.
func observeNotificationPause(response *http.Response, delay time.Duration, global bool) {
	if response.Request == nil {
		return
	}
	r, _ := response.Request.Context().Value(notificationRateKey{}).(*notificationRateRecorder)
	if r == nil {
		return
	}
	at := time.Now().UTC()
	r.record(NotificationRateState{NotBefore: at.Add(delay)}, global, at)
}
func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}
func (r *notificationRateRecorder) feedback(at time.Time) *NotificationRateFeedback {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := &NotificationRateFeedback{At: at.UTC()}
	for _, scope := range r.scopes {
		if state, ok := r.updates[scope.ID]; ok {
			state.NotBefore = maxTime(state.NotBefore, at)
			if !at.Before(state.SpacingUntil) {
				state.Spacing = 0
				state.SpacingUntil = time.Time{}
			}
			result.Updates = append(result.Updates, NotificationRateUpdate{ID: scope.ID, State: state})
		}
	}
	return result
}

// Receipt reads belong to an already accepted operation. Their waits consume
// that operation's original budget, and can never authorize another mutation.
func waitNotificationReceipt(ctx context.Context) error {
	r, _ := ctx.Value(notificationRateKey{}).(*notificationRateRecorder)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	next := r.nextRead
	r.mu.Unlock()
	if wait := time.Until(next); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}
