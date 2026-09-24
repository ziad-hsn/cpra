package management

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	bolt "go.etcd.io/bbolt"
)

func stageTestOptions(t *testing.T) StageOptions {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return StageOptions{Directory: directory, StoreID: "fixture-store", StageID: "fixture-stage", MaxResources: 2000, MaxEncodedBytes: 32 << 20}
}
func stageTestSealer(t *testing.T, key byte) *secureconfig.Sealer {
	t.Helper()
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{key}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}
func createTestStage(t *testing.T, options StageOptions, sealer *secureconfig.Sealer) *Stage {
	t.Helper()
	s, err := CreateStage(context.Background(), options, sealer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func addStage(t *testing.T, s *Stage, r api.Resource) {
	t.Helper()
	if err := s.Add(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}
func stageMonitor(id string) api.Resource {
	return resource("Monitor", id, api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health"}`)}}})
}
func stageFixture(secret string) []api.Resource {
	monitor := api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health"}`)}}, Notifications: api.Pointer(map[string]api.AlertRule{"red": {NotifyType: api.Pointer("webhook"), GroupRef: api.Pointer("ops")}})}
	return []api.Resource{
		resource("Monitor", "api", monitor),
		resource("NotificationGroup", "ops", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}),
		resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"hook"}}),
		resource("NotificationEndpoint", "hook", api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": "hook-url"})}),
		resource("Credential", "hook-url", api.CredentialSpec{Value: &secret}),
	}
}

func TestStageEncryptedRestartAndDependencyPages(t *testing.T) {
	ctx := context.Background()
	options := stageTestOptions(t)
	sealer := stageTestSealer(t, 61)
	s := createTestStage(t, options, sealer)
	secret := "https://example.test/notify?token=unique-bootstrap-secret"
	for _, r := range stageFixture(secret) {
		addStage(t, s, r)
	}
	if _, _, err := s.Page(ctx, "", 1); !errors.Is(err, ErrStageNotFrozen) {
		t.Fatalf("unvalidated stage readable: %v", err)
	}
	if err := s.Add(ctx, stageMonitor("api")); !errors.Is(err, ErrStageDuplicate) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	before, err := s.Freeze(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Count != 5 || before.Phase != "frozen" || len(before.Digest) != 64 || len(before.CatalogDigest) != 64 {
		t.Fatalf("invalid frozen metadata: %+v", before)
	}
	if err := s.Add(ctx, stageMonitor("new")); !errors.Is(err, ErrStageFrozen) {
		t.Fatalf("frozen stage mutable: %v", err)
	}
	var records []persistence.CatalogRecord
	cursor := ""
	firstCursor := ""
	for {
		page, next, err := s.Page(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, page...)
		if cursor == "" {
			firstCursor = next
		}
		if next == "" {
			break
		}
		cursor = next
	}
	digest := persistence.BootstrapInitialDigest()
	for _, record := range records {
		var err error
		digest, err = persistence.BootstrapDigest(digest, record)
		if err != nil {
			t.Fatal(err)
		}
	}
	if digest != before.CatalogDigest {
		t.Fatal("frozen activation digest does not match seed records")
	}
	for i, kind := range ResourceKinds() {
		if records[i].Key.Kind != kind {
			t.Fatalf("wrong dependency order: %+v", records)
		}
	}
	for _, record := range records {
		plain, err := sealer.Open(ctx, record.Binding(options.StoreID), record.Payload)
		if err != nil {
			t.Fatal("not seed-ready ciphertext", err)
		}
		var decoded api.Resource
		if api.StrictDecode(plain, &decoded) != nil || decoded.Metadata.UID != record.UID || decoded.Metadata.ResourceVersion != record.Revision || decoded.Metadata.Generation != 1 {
			t.Fatal("identity not frozen with ciphertext")
		}
		clear(plain)
	}
	raw, err := os.ReadFile(filepath.Join(options.Directory, stageFileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte("unique-bootstrap-secret")) {
		t.Fatal("plaintext secret written to stage")
	}
	if _, err := json.Marshal(s); err == nil {
		t.Fatal("stage serializable")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", s, s), secret) {
		t.Fatal("stage formatting exposed secret")
	}
	if _, err := ResumeStage(ctx, options, sealer); !errors.Is(err, ErrStageLocked) {
		t.Fatalf("simultaneous stage open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := ResumeStage(ctx, options, sealer)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.Info()
	if err != nil || before != after {
		t.Fatalf("identity changed on resume: %+v %v", after, err)
	}
	all, _, err := reopened.Page(ctx, "", 500)
	if err != nil || !reflect.DeepEqual(all, records) {
		t.Fatal("ciphertext or identity changed on resume", err)
	}
	page, next, err := reopened.Page(ctx, firstCursor, 2)
	if err != nil || !reflect.DeepEqual(page, records[2:4]) || next == "" {
		t.Fatal("frozen page cursor changed on resume", err)
	}
}

func TestStageMissingFinalReferenceAndSemanticFailureNeverActivate(t *testing.T) {
	catalog, store := testCatalog(t)
	ctx := context.Background()
	for _, scenario := range []string{"missing reference", "bad final endpoint", "mismatched notification driver"} {
		t.Run(scenario, func(t *testing.T) {
			s := createTestStage(t, stageTestOptions(t), stageTestSealer(t, 62))
			fixtures := stageFixture("https://example.test/notify")
			for i, r := range fixtures {
				if scenario == "missing reference" && i == 4 {
					continue
				}
				if scenario == "bad final endpoint" && r.Kind == "Credential" {
					r = resource("Credential", "hook-url", api.CredentialSpec{Value: api.Pointer("not-a-valid-url")})
				}
				if scenario == "mismatched notification driver" && r.Kind == "Monitor" {
					var spec api.MonitorSpec
					_ = json.Unmarshal(r.Spec, &spec)
					(*spec.Notifications)["red"] = api.AlertRule{NotifyType: api.Pointer("email"), GroupRef: api.Pointer("ops")}
					r = resource("Monitor", "api", spec)
				}
				addStage(t, s, r)
			}
			if _, err := s.Freeze(ctx); !errors.Is(err, ErrValidation) {
				t.Fatalf("invalid complete graph accepted: %v", err)
			}
			info, _ := s.Info()
			if info.Phase != "loading" {
				t.Fatal("failed validation froze stage")
			}
			view, err := store.CatalogSnapshot()
			if err != nil || view.Len() != 0 || !catalog.Ready() {
				t.Fatal("staging changed active catalog or readiness")
			}
			if scenario == "missing reference" {
				addStage(t, s, fixtures[4])
				if _, err := s.Freeze(ctx); err != nil {
					t.Fatal("valid completed input could not freeze", err)
				}
			}
		})
	}
}

func TestStageQuotaAndOversizedResourceLeaveInventoryUnchanged(t *testing.T) {
	ctx := context.Background()
	sealer := stageTestSealer(t, 63)
	options := stageTestOptions(t)
	options.MaxResources = 1
	s := createTestStage(t, options, sealer)
	addStage(t, s, stageMonitor("one"))
	before, _ := s.Info()
	if err := s.Add(ctx, stageMonitor("two")); !errors.Is(err, ErrStageQuota) {
		t.Fatalf("count quota bypassed: %v", err)
	}
	after, _ := s.Info()
	if before != after {
		t.Fatal("rejected append changed inventory")
	}
	bytesOptions := stageTestOptions(t)
	bytesOptions.MaxEncodedBytes = stageMetadataBudget + 1
	bounded := createTestStage(t, bytesOptions, sealer)
	if err := bounded.Add(ctx, stageMonitor("one")); !errors.Is(err, ErrStageQuota) {
		t.Fatalf("byte quota bypassed: %v", err)
	}
	info, _ := bounded.Info()
	if info.Count != 0 || info.EncodedBytes != stageMetadataBudget {
		t.Fatal("byte quota partially persisted")
	}
	large := createTestStage(t, stageTestOptions(t), sealer)
	secret := strings.Repeat("x", secureconfig.MaxPlaintext-1024)
	addStage(t, large, resource("Credential", "large", api.CredentialSpec{Value: &secret}))
	if _, err := large.Freeze(ctx); err != nil {
		t.Fatal("near-limit resource lost to nested encryption overhead", err)
	}
	tooLarge := strings.Repeat("z", api.MaxResourceBytes)
	fresh := createTestStage(t, stageTestOptions(t), sealer)
	if err := fresh.Add(ctx, resource("Credential", "oversized", api.CredentialSpec{Value: &tooLarge})); !errors.Is(err, ErrValidation) {
		t.Fatalf("resource overflow accepted: %v", err)
	}
}

func TestStageResumeRejectsIdentityKeyFormatAndInventoryTampering(t *testing.T) {
	ctx := context.Background()
	cases := []string{"wrong key", "wrong store", "wrong stage", "wrong limits", "unsupported format", "wrong catalog digest", "row deletion", "extra row", "missing index", "reordered index", "swapped record", "missing stamp", "bad payload", "empty database"}
	for _, scenario := range cases {
		t.Run(scenario, func(t *testing.T) {
			o := stageTestOptions(t)
			sealer := stageTestSealer(t, 64)
			s := createTestStage(t, o, sealer)
			addStage(t, s, stageMonitor("one"))
			addStage(t, s, stageMonitor("two"))
			if _, err := s.Freeze(ctx); err != nil {
				t.Fatal(err)
			}
			if scenario == "unsupported format" || scenario == "wrong catalog digest" {
				next := s.info
				if scenario == "unsupported format" {
					next.Format++
				} else {
					next.CatalogDigest = strings.Repeat("0", 64)
				}
				raw, err := s.encodeInfo(ctx, next)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(stageMetaBucket).Put(stageMetaKey, raw) }); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "row deletion" || scenario == "extra row" || scenario == "missing index" || scenario == "reordered index" || scenario == "swapped record" || scenario == "missing stamp" || scenario == "bad payload" {
				err := s.db.Update(func(tx *bolt.Tx) error {
					records, order := tx.Bucket(stageRecordsBucket), tx.Bucket(stageOrderBucket)
					one, two := []byte("4:one"), []byte("4:two")
					switch scenario {
					case "row deletion":
						return records.Delete(one)
					case "extra row":
						return records.Put([]byte("4:extra"), bytes.Clone(records.Get(one)))
					case "missing index":
						var key [8]byte
						binary.BigEndian.PutUint64(key[:], 1)
						return order.Delete(key[:])
					case "reordered index":
						var key [8]byte
						binary.BigEndian.PutUint64(key[:], 1)
						return order.Put(key[:], two)
					case "swapped record":
						return records.Put(one, bytes.Clone(records.Get(two)))
					default:
						var entry stageEntry
						_ = json.Unmarshal(records.Get(one), &entry)
						if scenario == "missing stamp" {
							entry.Stamp = secureconfig.Envelope{}
						} else {
							entry.Record.Payload.Ciphertext[0] ^= 1
						}
						raw, _ := json.Marshal(entry)
						return records.Put(one, raw)
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong key":
				sealer = stageTestSealer(t, 65)
			case "wrong store":
				o.StoreID = "other-store"
			case "wrong stage":
				o.StageID = "other-stage"
			case "wrong limits":
				o.MaxResources++
			case "empty database":
				if err := os.WriteFile(filepath.Join(o.Directory, stageFileName), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			resumed, err := ResumeStage(ctx, o, sealer)
			if resumed != nil {
				_ = resumed.Close()
			}
			if !errors.Is(err, ErrStageUnavailable) {
				t.Fatalf("tampered stage resumed: %v", err)
			}
		})
	}
}

func TestStageRequiresPrivateExistingDirectoryAndNeverCreatesOnResume(t *testing.T) {
	ctx := context.Background()
	sealer := stageTestSealer(t, 66)
	o := stageTestOptions(t)
	if _, err := ResumeStage(ctx, o, sealer); !errors.Is(err, ErrStageMissing) {
		t.Fatalf("missing resume: %v", err)
	}
	entries, err := os.ReadDir(o.Directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("resume created files")
	}
	s := createTestStage(t, o, sealer)
	if _, err := CreateStage(ctx, o, sealer); !errors.Is(err, ErrStageExists) {
		t.Fatalf("overwrote existing stage: %v", err)
	}
	if runtime.GOOS != "windows" {
		bad := stageTestOptions(t)
		if err := os.Chmod(bad.Directory, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateStage(ctx, bad, sealer); !errors.Is(err, ErrStageUnavailable) {
			t.Fatalf("shared directory accepted: %v", err)
		}
		if err := os.Remove(filepath.Join(o.Directory, stageFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Info(); !errors.Is(err, ErrStageUnavailable) {
			t.Fatalf("missing open database not detected: %v", err)
		}
	}
}

func TestStageDecodePipelineAndValidationPerformNoProviderIO(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	s := createTestStage(t, stageTestOptions(t), stageTestSealer(t, 67))
	m := stageMonitor("api")
	var spec api.MonitorSpec
	_ = json.Unmarshal(m.Spec, &spec)
	spec.Check.Driver.Config, _ = json.Marshal(map[string]string{"url": target.URL})
	m.Spec, _ = json.Marshal(spec)
	raw, _ := json.Marshal(m)
	input := append(append(raw, '\n'), []byte("{malformed-final-document")...)
	err := collection.Decode(ctx, bytes.NewReader(input), collection.DecodeOptions{SourceName: "fixture"}, func(item collection.Item) error { return s.Add(ctx, item.Resource) })
	if err == nil {
		t.Fatal("malformed final document accepted")
	}
	if _, _, err := s.Page(ctx, "", 100); !errors.Is(err, ErrStageNotFrozen) {
		t.Fatal("partial decode activated resources")
	}
	if calls.Load() != 0 {
		t.Fatal("staging invoked check provider")
	}
	clean := createTestStage(t, stageTestOptions(t), stageTestSealer(t, 68))
	if err := collection.Decode(ctx, bytes.NewReader(raw), collection.DecodeOptions{}, func(item collection.Item) error { return clean.Add(ctx, item.Resource) }); err != nil {
		t.Fatal(err)
	}
	if _, err := clean.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("validation invoked check provider")
	}
}

func TestStageAll33DriverValidationUsesCompiledCapabilities(t *testing.T) {
	fixtures := []struct{ category, driver, config string }{
		{"check", "http", `{"url":"https://example.test"}`}, {"check", "tcp", `{"host":"host","port":80}`}, {"check", "udp", `{"host":"host","port":80}`}, {"check", "dns", `{"host":"host"}`}, {"check", "icmp", `{"host":"host"}`}, {"check", "grpc", `{"host":"host","port":80}`}, {"check", "docker", `{"container":"fixture"}`}, {"check", "tls", `{"host":"host","port":443}`},
		{"check", "redis", `{}`}, {"check", "postgres", `{}`}, {"check", "mysql", `{}`}, {"check", "mongo", `{"uri":"mongodb://host"}`}, {"check", "rabbitmq", `{"url":"amqp://host"}`}, {"check", "kafka", `{"brokers":["host:9092"]}`},
		{"recovery", "docker", `{"container":"fixture"}`}, {"recovery", "webhook", `{"url":"https://example.test"}`}, {"recovery", "kubernetes", `{"namespace":"fixture","kind":"deployment","name":"test"}`}, {"recovery", "aws", `{"operation":"reboot-instance","instanceId":"i-fixture"}`}, {"recovery", "systemd", `{"unit":"fixture.service"}`},
		{"notification", "log", `{"file":"fixture.log"}`}, {"notification", "email", `{"server":"host:25","to":"ops@example.test","from":"cpra@example.test"}`}, {"notification", "webhook", `{"url":"https://example.test"}`}, {"notification", "slack", `{"hook":"https://example.test"}`}, {"notification", "pagerduty", `{"routingKey":"fixture"}`}, {"notification", "telegram", `{"botToken":"fixture","chatId":"fixture"}`}, {"notification", "discord", `{"webhookUrl":"https://example.test"}`}, {"notification", "opsgenie", `{"apiKey":"fixture"}`}, {"notification", "teams", `{"webhookUrl":"https://example.test"}`}, {"notification", "mattermost", `{"webhookUrl":"https://example.test"}`}, {"notification", "pushover", `{"appToken":"fixture","userKey":"fixture"}`}, {"notification", "twilio", `{"accountSid":"fixture","authToken":"fixture","from":"+15550001","to":"+15550002"}`}, {"notification", "datadog", `{"apiKey":"fixture"}`}, {"notification", "victorops", `{"restEndpointKey":"fixture","routingKey":"fixture"}`},
	}
	if len(fixtures) != 33 {
		t.Fatal("incomplete fixture inventory")
	}
	for _, fixture := range fixtures {
		t.Run(fixture.category+"/"+fixture.driver, func(t *testing.T) {
			s := createTestStage(t, stageTestOptions(t), stageTestSealer(t, 69))
			driver := api.DriverConfig{Type: fixture.driver, Config: json.RawMessage(fixture.config)}
			var input api.Resource
			if fixture.category == "notification" {
				input = resource("NotificationEndpoint", "fixture", driver)
			} else {
				var spec api.MonitorSpec
				_ = json.Unmarshal(stageMonitor("fixture").Spec, &spec)
				if fixture.category == "check" {
					spec.Check.Driver = driver
				} else {
					spec.Recovery = &api.RecoverySpec{Driver: driver}
				}
				input = resource("Monitor", "fixture", spec)
			}
			normalized, credentials, err := ExtractInlineCredentials(input)
			if err != nil {
				t.Fatal(err)
			}
			for _, credential := range credentials {
				addStage(t, s, credential)
			}
			err = s.Add(context.Background(), normalized)
			if jobs.ValidateDriver(fixture.category, fixture.driver) != nil {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("uncompiled driver not rejected: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Freeze(context.Background()); err != nil {
				t.Fatal("compiled driver semantic validation failed", err)
			}
		})
	}
}

func TestStageProcessChild(t *testing.T) {
	directory := os.Getenv("CPRA_STAGE_CHILD_DIRECTORY")
	if directory == "" {
		t.Skip("separate-process crash helper")
	}
	options := StageOptions{Directory: directory, StoreID: "fixture-store", StageID: "crash-stage", MaxResources: 10, MaxEncodedBytes: 4 << 20}
	stage, err := CreateStage(context.Background(), options, stageTestSealer(t, 71))
	if err != nil {
		t.Fatal(err)
	}
	secret := "crash-committed-private-credential"
	if err := stage.Add(context.Background(), resource("Credential", "committed", api.CredentialSpec{Value: &secret})); err != nil {
		t.Fatal(err)
	}
	fmt.Println("stage-committed")
	select {}
}

func TestStageRecoversCommittedAppendAfterProcessKill(t *testing.T) {
	options := stageTestOptions(t)
	options.StageID, options.MaxResources, options.MaxEncodedBytes = "crash-stage", 10, 4<<20
	command := exec.Command(os.Args[0], "-test.run=^TestStageProcessChild$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "CPRA_STAGE_CHILD_DIRECTORY="+options.Directory)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if scanner.Text() == "stage-committed" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatalf("child failed before durable append: %s", stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("child did not report committed staging")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	sealer := stageTestSealer(t, 71)
	resumed, err := ResumeStage(context.Background(), options, sealer)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	info, err := resumed.Freeze(context.Background())
	if err != nil || info.Count != 1 {
		t.Fatalf("committed append lost: %+v %v", info, err)
	}
	records, _, err := resumed.Page(context.Background(), "", 100)
	if err != nil || len(records) != 1 || records[0].Key.ID != "committed" {
		t.Fatal("crash recovery missing original record", err)
	}
}

func TestStagePageByteBoundsAndCancellation(t *testing.T) {
	ctx := context.Background()
	stage := createTestStage(t, stageTestOptions(t), stageTestSealer(t, 72))
	value := strings.Repeat("x", 700<<10)
	for i := range 6 {
		addStage(t, stage, resource("Credential", fmt.Sprintf("large-%d", i), api.CredentialSpec{Value: &value}))
	}
	if _, err := stage.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	records, next, err := stage.Page(ctx, "", 500)
	if err != nil || len(records) == 0 || len(records) >= 6 || next == "" {
		t.Fatalf("page ignored byte ceiling: %d %v", len(records), err)
	}
	cursor := next
	count := len(records)
	for cursor != "" {
		page, next, err := stage.Page(ctx, cursor, 500)
		if err != nil {
			t.Fatal(err)
		}
		count += len(page)
		cursor = next
	}
	if count != 6 {
		t.Fatal("byte-bounded pages omitted records")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := stage.Page(canceled, "", 100); !errors.Is(err, context.Canceled) {
		t.Fatal("page ignored context", err)
	}
	if _, err := stage.Freeze(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("freeze ignored context", err)
	}
	if _, _, err := stage.Page(ctx, "untrusted-fixture-cursor", 100); !errors.Is(err, ErrValidation) {
		t.Fatal("invalid cursor accepted", err)
	}
}

func TestStageIntentionalEmptyInputAndCanceledFreeze(t *testing.T) {
	stage := createTestStage(t, stageTestOptions(t), stageTestSealer(t, 73))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stage.Freeze(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("empty freeze ignored cancellation", err)
	}
	info, err := stage.Freeze(context.Background())
	if err != nil || info.Count != 0 || info.Phase != "frozen" || info.CatalogDigest != persistence.BootstrapInitialDigest() {
		t.Fatalf("intentional empty input failed: %+v %v", info, err)
	}
	if records, next, err := stage.Page(context.Background(), "", 100); err != nil || len(records) != 0 || next != "" {
		t.Fatal("empty page inconsistent", err)
	}
	if _, err := stage.Freeze(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("frozen empty stage ignored cancellation", err)
	}
}
