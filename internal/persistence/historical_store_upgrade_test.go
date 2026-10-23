package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

// These fixtures are produced by the actual archived commits, using the helper
// and offline workspace recorded beside each fixture. This opt-in campaign
// never opens the originals or constructs any controller, job or provider.
func TestHistoricalCommittedStoreUpgrade(t *testing.T) {
	root := os.Getenv("CPRA_OLD_STORE_FIXTURES")
	if root == "" {
		t.Skip("requires separately recorded historical-source storage fixtures")
	}
	for _, commit := range []string{"b293f34dd57ee61c78371643de6d0ebaa75c626e", "0a22680c48e59e054fbf298d6f99c5ac62342e5a"} {
		t.Run(commit[:7], func(t *testing.T) {
			directory := filepath.Join(root, "old-store-"+commit[:7])
			config := runtimeconfig.Default()
			config.Storage.Directory = t.TempDir()
			originals := copyHistoricalStore(t, directory, commit, config.Storage.Directory)
			store, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal("open historical committed store:", err)
			}
			defer func() {
				if store != nil {
					_ = store.Close()
				}
			}()
			at := time.Now().UTC()
			for _, original := range originals {
				t.Run(original.ID, func(t *testing.T) {
					m, ok := store.Get(original.ID)
					if !ok {
						t.Fatal("historical monitor was lost")
					}
					before := map[string]Action{}
					for id, old := range original.Actions {
						current := historicalAction(t, store, id)
						want := old.State
						if want == Started {
							want = Unknown
						}
						if old.State != Queued && current.State != want {
							t.Fatalf("historical state changed: %s -> %s", old.State, current.State)
						}
						if old.State == Queued && current.State != Queued && (current.State != Cancelled || !current.StartedAt.IsZero() ||
							!slices.Contains([]string{"notification_format_upgraded", "notification_identity_incomplete", "notification_deadline_unrecorded", "notification_deadline"}, current.Outcome)) {
							t.Fatal("historical queued action acquired an unsafe outcome", current.State, current.Outcome)
						}
						if old.State == Unknown && !current.Held() || old.State == Started && !current.Held() {
							t.Fatal("restart released an uncertain outcome")
						}
						if current.ID != old.ID || current.Revision != old.Revision || current.IncidentID != old.IncidentID ||
							!current.StartedAt.Equal(old.StartedAt) || !current.Deadline.Equal(old.Deadline) {
							t.Fatal("restart rewrote historical identity, start or fixed budget")
						}
						before[id] = current
					}
					configuration := m.Clone()
					configuration.Policy.NotificationEndpointIDs = map[string][]string{"red": {"fixture-red"}, "green": {"fixture-red"}}
					configuration.Policy.NotificationDrivers = map[string][]string{"red": {"pagerduty"}, "green": {"pagerduty"}}
					if configuration.Policy.Endpoints["yellow"] != 0 {
						configuration.Policy.NotificationEndpointIDs["yellow"] = []string{"fixture-yellow"}
						configuration.Policy.NotificationDrivers["yellow"] = []string{"log"}
					}
					result := submit(t, store, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &configuration, At: at})[0]
					if result.Err != nil || !result.Allowed {
						t.Fatal("configure historical binding:", result.Err)
					}
					m, _ = store.Get(m.ID)
					if original.ID == "red-queued" && m.greenEligible() {
						t.Fatal("unstarted historical notification manufactured exposure")
					}
					for id, old := range before {
						if old.State != Queued && !reflect.DeepEqual(historicalAction(t, store, id), old) {
							t.Fatal("configuration binding rewrote retained provider facts", id)
						}
					}
					pulse := func(outcome string) {
						m, _ = store.Get(m.ID)
						at = at.Add(time.Second)
						result := submit(t, store, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at, Scheduled: at, Outcome: outcome})[0]
						if result.Err != nil {
							t.Fatal("observe after upgrade:", result.Err)
						}
						m, _ = store.Get(m.ID)
					}
					if original.ID != "red-queued" && original.ID != "green-complete" {
						pulse("failure")
						if queuedColorCount(m, "red") != 0 {
							t.Fatal("upgrade queued a duplicate red for an exposed outage")
						}
					}
					pulse("success")
					pulse("success")
					if m.Incident {
						t.Fatal("health recovery was blocked by retained action evidence")
					}
					if original.ID == "red-queued" || original.ID == "green-complete" {
						if queuedColorCount(m, "green") != 0 {
							t.Fatal("upgrade manufactured or repeated a green delivery")
						}
					} else {
						green := notificationByColor(t, m, "green", Queued)
						if strings.HasPrefix(commit, "b293f34") {
							if green.ProviderKey != "cpra:"+original.Name {
								t.Fatal("pre-40 PagerDuty closure lost the literal original key", green.ProviderKey)
							}
						} else {
							var opening Action
							for _, a := range original.Actions {
								if a.Color == "red" {
									opening = a
								}
							}
							if green.ProviderKey != "" || green.NotificationCorrelation != opening.NotificationCorrelation || green.ProviderCorrelationVersion != opening.ProviderCorrelationVersion {
								t.Fatal("format-42 closure changed the provider correlation inputs")
							}
						}
						t.Logf("closure retained old provider identity; incident=%s", green.IncidentID)
					}
					for id, old := range before {
						if old.State == Unknown && !historicalAction(t, store, id).Held() {
							t.Fatal("healthy observation released uncertain delivery evidence")
						}
					}
				})
			}
			before := historicalActions(t, store)
			if err := store.Snapshot(); err != nil {
				t.Fatal("snapshot upgraded store:", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(t.Context(), config)
			if err != nil {
				t.Fatal("restart upgraded snapshot:", err)
			}
			if after := historicalActions(t, store); !reflect.DeepEqual(before, after) {
				t.Fatal("snapshot restart changed retained action facts or queued closure identities")
			}
			for _, original := range originals {
				page, err := store.History().Page(original.ID, "", 500)
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range page.Events {
					if event.Type == "action_started" {
						old, ok := original.Actions[event.ActionID]
						if !ok || old.StartedAt.IsZero() {
							t.Fatal("migration committed a new external-execution start")
						}
					}
				}
			}
			t.Logf("source=%s monitors=%d actions=%d provider executions=0", commit, len(originals), len(before))
		})
	}
}

func historicalAction(t *testing.T, store *Store, id string) Action {
	t.Helper()
	r, ok, err := store.Action(id)
	if err != nil || !ok {
		t.Fatal("historical action lookup:", id, err)
	}
	return r.Action
}

func historicalActions(t *testing.T, store *Store) map[string]Action {
	t.Helper()
	view, err := store.ActionSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	rows, next, err := view.Page("", 100)
	if err != nil || next != "" {
		t.Fatal("unexpected historical fixture action page:", err)
	}
	result := make(map[string]Action, len(rows))
	for _, row := range rows {
		result[row.Action.ID] = row.Action
	}
	return result
}

func copyHistoricalStore(t *testing.T, directory, commit, destination string) []Monitor {
	t.Helper()
	var manifest struct {
		SourceCommit string `json:"source_commit"`
		Files        map[string]struct {
			SHA256 string `json:"sha256"`
			Bytes  int64  `json:"bytes"`
		} `json:"files"`
	}
	data, err := os.ReadFile(filepath.Join(directory, "fixture-current-manifest.json"))
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.SourceCommit != commit || len(manifest.Files) == 0 || len(manifest.Files) > 100 {
		t.Fatal("missing or mismatched historical fixture manifest:", err)
	}
	verify := func() {
		t.Helper()
		for name, expected := range manifest.Files {
			if !filepath.IsLocal(name) || expected.Bytes < 0 || expected.Bytes > 16<<20 {
				t.Fatal("invalid historical fixture file bound")
			}
			file, err := os.Open(filepath.Join(directory, "output-current", name))
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.New()
			n, readErr := io.Copy(hash, io.LimitReader(file, expected.Bytes+1))
			closeErr := file.Close()
			if readErr != nil || closeErr != nil || n != expected.Bytes || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
				t.Fatal("historical fixture bytes changed:", name)
			}
		}
	}
	verify()
	t.Cleanup(verify)
	for name := range manifest.Files {
		if !strings.HasPrefix(name, "state/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, "output-current", name))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(destination, strings.TrimPrefix(name, "state/"))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var committed struct {
		SourceCommit string    `json:"source_commit"`
		Cases        []Monitor `json:"cases"`
	}
	data, err = os.ReadFile(filepath.Join(directory, "output-current", "committed.json"))
	if err != nil || json.Unmarshal(data, &committed) != nil || committed.SourceCommit != commit || len(committed.Cases) != 6 {
		t.Fatal("invalid historical committed-state report:", err)
	}
	return committed.Cases
}
