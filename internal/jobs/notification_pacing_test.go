package jobs

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

func TestNotificationPacingNeverChangesEffectEvidence(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(status)
			}))
			defer receiver.Close()
			d := NewDispatch(&CodeWebhookJob{URL: receiver.URL}, ecs.Entity{}, "code", "red", 0, 0)
			d.Driver = "webhook"
			d.ConfigureNotificationPacing(rateIdentity(receiver.URL), 0, "")
			r := d.Execute()
			if calls != 1 || r.RateFeedback == nil {
				t.Fatal("invocation accounting", calls)
			}
			effect := ClassifyEffect(r.Err)
			if status == 200 && effect.Outcome != EffectSucceeded || status != 200 && effect.Outcome != EffectUnknown || effect.Retryable {
				t.Fatal("pacing altered effect classification", effect)
			}
			if status == 429 || status == 503 {
				if len(r.RateFeedback.Updates) != 1 || !r.RateFeedback.Updates[0].State.NotBefore.After(r.ExecutionEnd) {
					t.Fatal("response pause lost")
				}
			}
		})
	}
}
func TestDiscordPacingReadsSuccessfulQuotaAndMajorResource(t *testing.T) {
	dispatch := func(target string) *Dispatch {
		d := NewDispatch(&CodeDiscordJob{WebhookURL: target}, ecs.Entity{}, "code", "red", 0, 0)
		d.Driver = "discord"
		d.ConfigureNotificationPacing(rateIdentity(target), 0, "")
		return d
	}
	a, b, c := dispatch("https://discord.com/api/webhooks/one/token?thread_id=1"), dispatch("https://DISCORD.COM:443/api/v10/webhooks/one/token?thread_id=2"), dispatch("https://discord.com/api/webhooks/two/token")
	if a.RateScopes[0].ID != b.RateScopes[0].ID || a.RateScopes[0].ID == c.RateScopes[0].ID || a.RateScopes[1].ID != c.RateScopes[1].ID {
		t.Fatal("wrong major-resource/global scope")
	}
	at := time.Now().UTC()
	recorder := a.rateRecorder()
	h := http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset-After": {"0.125"}}
	recorder.observe(h, 204, at)
	f := recorder.feedback(at)
	if len(f.Updates) != 1 || f.Updates[0].ID != a.RateScopes[0].ID || f.Updates[0].State.NotBefore != at.Add(125*time.Millisecond) {
		t.Fatal("successful fractional exhaustion lost", f)
	}
	h.Set("X-RateLimit-Global", "true")
	h.Set("Retry-After", "1")
	recorder.observe(h, 429, at)
	f = recorder.feedback(at)
	if len(f.Updates) != 2 || f.Updates[1].ID != a.RateScopes[1].ID || f.Updates[1].State.NotBefore.Before(at.Add(time.Second)) {
		t.Fatal("global cooldown missing", f)
	}
	// A shorter, late observation cannot reopen the scope early.
	h.Set("Retry-After", "0")
	recorder.observe(h, 429, at.Add(10*time.Millisecond))
	if recorder.feedback(at).Updates[0].State.NotBefore.Before(at.Add(time.Second)) {
		t.Fatal("late observation shortened pause")
	}
}

func TestDiscordPacingUsesBoundedBodyOnlyGlobalDelay(t *testing.T) {
	var calls atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"retry_after":30,"global":true}`)
	}))
	defer receiver.Close()
	d := NewDispatch(&CodeDiscordJob{WebhookURL: receiver.URL + "/api/webhooks/one/token"}, ecs.Entity{}, "code", "red", 0, 0)
	d.Driver = "discord"
	d.ConfigureNotificationPacing(rateIdentity(receiver.URL), 0, "")
	r := d.Execute()
	if calls.Load() != 1 || r.RateFeedback == nil || len(r.RateFeedback.Updates) != 2 {
		t.Fatal("body-only global pause missing", calls.Load(), r.RateFeedback)
	}
	for _, u := range r.RateFeedback.Updates {
		if u.State.NotBefore.Sub(r.ExecutionEnd) < 29*time.Second {
			t.Fatal("body delay lost")
		}
	}
	if e := ClassifyEffect(r.Err); e.Outcome != EffectNotApplied || !e.Retryable {
		t.Fatal("documented rejection classification changed", e)
	}
}

func TestOpsgeniePacingHoldsAcceptedOperationWithoutRepeatingPost(t *testing.T) {
	var posts, gets atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
			w.Header().Set("X-RateLimit-State", "BURST")
			w.Header().Set("X-RateLimit-Period-In-Sec", "60")
			w.WriteHeader(202)
			fmt.Fprint(w, `{"requestId":"11111111-1111-4111-8111-111111111111"}`)
			return
		}
		gets.Add(1)
		fmt.Fprint(w, `{"data":{"success":true,"isSuccess":true}}`)
	}))
	defer receiver.Close()
	d := NewDispatch(&CodeOpsgenieJob{URL: receiver.URL + "/v2/alerts", APIKey: "synthetic"}, ecs.Entity{}, "code", "red", 0, 0)
	d.Driver, d.Deadline = "opsgenie", time.Now().Add(100*time.Millisecond)
	d.ConfigureNotificationPacing(rateIdentity(receiver.URL), 0, "account")
	if !d.RateScopes[0].Serial || !d.RateScopes[1].Serial {
		t.Fatal("accepted operation must retain account reservations")
	}
	r := d.Execute()
	if posts.Load() != 1 || gets.Load() != 0 || ClassifyEffect(r.Err).Outcome != EffectUnknown || len(r.RateFeedback.Updates) != 2 {
		t.Fatal("accepted operation was replayed or pause lost", posts.Load(), gets.Load(), r.Err)
	}
	other := NewDispatch(&CodeOpsgenieJob{URL: receiver.URL + "/v2/alerts", APIKey: "different-account"}, ecs.Entity{}, "code", "red", 0, 0)
	other.Driver = "opsgenie"
	other.ConfigureNotificationPacing(rateIdentity(receiver.URL), 0, "account")
	if d.RateScopes[0].ID == other.RateScopes[0].ID || d.RateScopes[1].ID != other.RateScopes[1].ID {
		t.Fatal("account/key and explicit sharing identities conflated")
	}
}
func TestOpsgenieAcceptedBurstIsPacingNotRejection(t *testing.T) {
	d := NewDispatch(&CodeOpsgenieJob{URL: "https://api.opsgenie.com/v2/alerts", APIKey: "synthetic"}, ecs.Entity{}, "code", "red", 0, 0)
	d.Driver = "opsgenie"
	d.ConfigureNotificationPacing(rateIdentity("integration"), 0, "account")
	at := time.Now().UTC()
	r := d.rateRecorder()
	h := http.Header{}
	h.Set("X-RateLimit-State", "BURST")
	h.Set("X-RateLimit-Period-In-Sec", "60")
	r.observe(h, 202, at)
	if len(r.feedback(at).Updates) != 2 || r.feedback(at).Updates[0].State.NotBefore != at.Add(time.Minute) {
		t.Fatal("shared account pause lost")
	}
	if err := notificationResponseError("opsgenie", &http.Response{StatusCode: 202, Header: h}); err != nil {
		t.Fatal("accepted delayed processing became rejection", err)
	}
}
