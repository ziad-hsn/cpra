//go:build externaljobs

package main

import (
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerLoadTargetKeepsRequiredEffectsEligible(t *testing.T) {
	for _, recoveryFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "notification-first", true: "recovery-first"}[recoveryFirst], func(t *testing.T) {
			var episode workerLoadEpisode
			check := workerLoadInvocation{Kind: "check"}
			assertChecks := func(want string) {
				t.Helper()
				for range 20 {
					if got, err := episode.complete(check); err != nil || got != want {
						t.Fatalf("checks changed the episode without its required effects: %q, %v", got, err)
					}
				}
			}
			yellow := workerLoadInvocation{Kind: "notification", Notification: &api.WorkerNotification{
				Color: "yellow", IncidentID: "incident", CorrelationID: "correlation", EndpointID: "endpoint", DeliveryID: "yellow-delivery",
			}}
			recovery := workerLoadInvocation{Kind: "recovery"}
			first, second := yellow, recovery
			if recoveryFirst {
				first, second = recovery, yellow
			}
			assertChecks("failure")
			if _, err := episode.complete(first); err != nil {
				t.Fatal(err)
			}
			assertChecks("failure")
			if _, err := episode.complete(second); err != nil {
				t.Fatal(err)
			}
			assertChecks("success")
			green := *yellow.Notification
			green.Color, green.DeliveryID, green.EndpointID = "green", "green-delivery", "green-endpoint"
			wrong := green
			wrong.CorrelationID = "other-outage"
			if _, err := episode.complete(workerLoadInvocation{Kind: "notification", Notification: &wrong}); err == nil {
				t.Fatal("unrelated green advanced the target episode")
			}
			assertChecks("success")
			if got, err := episode.complete(workerLoadInvocation{Kind: "notification", Notification: &green}); err != nil || got != "delivered" {
				t.Fatal(got, err)
			}
			if episode.closed != 1 {
				t.Fatal("matching effects did not complete exactly one target episode")
			}
			assertChecks("failure")
		})
	}
}
