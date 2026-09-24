package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	v2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const groupARN = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/payments/abc"

func testSettings() settings {
	return settings{Account: "123456789012", Region: "us-east-1", Mappings: []mapping{{
		TargetGroupARN: groupARN, TargetID: "i-123", MonitorID: "payments-node", MonitorUID: "uid-1", ResourceVersion: "rv-1",
		EventsAfter: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}}}
}

func eventBody() string {
	return `{"id":"event-1","source":"aws.elasticloadbalancing","detail-type":"AWS API Call via CloudTrail","account":"123456789012","region":"us-east-1","detail":{"eventID":"trail-1","eventSource":"elasticloadbalancing.amazonaws.com","eventName":"DeregisterTargets","awsRegion":"us-east-1","eventTime":"2026-09-13T12:00:00Z","requestParameters":{"targetGroupArn":"` + groupARN + `","targets":[{"id":"i-123"}]}}}`
}

// The fake AWS read is explicit; CPRa reads/writes still use the public SDK over HTTPS.
type registrationFixture struct {
	confirmed bool
	err       error
	calls     int
}

func (f *registrationFixture) absent(context.Context, mapping) (bool, error) {
	f.calls++
	return f.confirmed, f.err
}

type monitorFixture struct {
	mu        sync.Mutex
	monitor   api.Monitor
	missing   bool
	patches   int
	get       int
	status    int
	loseReply bool
}

func newProcessor(t *testing.T) (*processor, *monitorFixture, *registrationFixture) {
	t.Helper()
	cfg := testSettings()
	f := &monitorFixture{monitor: api.Monitor{APIVersion: "cpra.io/v2", Kind: "Monitor", Metadata: api.Metadata{ID: "payments-node", UID: "uid-1", ResourceVersion: "rv-1"}, Spec: api.MonitorSpec{Enabled: aws.Bool(true), Check: api.CheckSpec{Driver: api.DriverConfig{Type: "http"}}}}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Path != "/api/v2/monitors/payments-node" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			f.get++
			if f.missing {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"status":404,"title":"not found"}`)
				return
			}
		} else if r.Method == http.MethodPatch {
			f.patches++
			raw, _ := io.ReadAll(r.Body)
			if string(raw) != `{"spec":{"enabled":false}}` || r.Header.Get("If-Match") != `"rv-1"` {
				t.Errorf("incorrect conditional disable: %s %q", raw, r.Header.Get("If-Match"))
			}
			if f.status != 0 {
				w.WriteHeader(f.status)
				io.WriteString(w, `{"status":412,"title":"version conflict"}`)
				return
			}
			f.monitor.Spec.Enabled = aws.Bool(false)
			f.monitor.Metadata.ResourceVersion = "rv-2"
			if f.loseReply {
				w.WriteHeader(http.StatusOK)
				return // A committed fixture mutation with an incomplete success reply.
			}
		} else {
			t.Errorf("unexpected method %s", r.Method)
		}
		json.NewEncoder(w).Encode(f.monitor)
	}))
	t.Cleanup(server.Close)
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: "fixture-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	registration := &registrationFixture{confirmed: true}
	return &processor{cfg: cfg, cpra: client, aws: registration, output: io.Discard}, f, registration
}

func TestConfirmedDeregistrationAndDuplicate(t *testing.T) {
	p, f, a := newProcessor(t)
	for range 2 {
		if err := p.process(context.Background(), eventBody()); err != nil {
			t.Fatal(err)
		}
	}
	if f.patches != 1 || a.calls != 1 || *f.monitor.Spec.Enabled {
		t.Fatalf("patches=%d AWS reads=%d enabled=%v", f.patches, a.calls, *f.monitor.Spec.Enabled)
	}
}

func TestLostDisableReplyDoesNotRepeatMutation(t *testing.T) {
	p, f, _ := newProcessor(t)
	f.loseReply = true
	if err := p.process(context.Background(), eventBody()); !errors.Is(err, cpra.ErrAmbiguous) {
		t.Fatalf("expected an uncertain mutation, got %v", err)
	}
	if err := p.process(context.Background(), eventBody()); err != nil {
		t.Fatal(err)
	}
	if f.patches != 1 {
		t.Fatalf("disable mutation repeated %d times", f.patches)
	}
}

func TestDemo(t *testing.T) {
	var output strings.Builder
	if err := runDemo(&output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "enabled=false") || !strings.Contains(output.String(), "already disabled") {
		t.Fatal("demo omitted verified result or duplicate handling")
	}
}

func TestAWSSDKQueryTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("Action") != "DescribeTargetHealth" || r.Form.Get("TargetGroupArn") != groupARN || r.Form.Get("Targets.member.1.Id") != "i-123" {
			t.Errorf("unexpected AWS Query request: %v", r.Form)
		}
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, `<DescribeTargetHealthResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"><DescribeTargetHealthResult><TargetHealthDescriptions><member><Target><Id>i-123</Id></Target><TargetHealth><State>unused</State><Reason>Target.NotRegistered</Reason></TargetHealth></member></TargetHealthDescriptions></DescribeTargetHealthResult></DescribeTargetHealthResponse>`)
	}))
	defer server.Close()
	fixtureCredentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "fixture-access", SecretAccessKey: "fixture-secret", Source: "local-contract-fixture"}, nil
	})
	client := elasticloadbalancingv2.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: fixtureCredentials, HTTPClient: server.Client()}, func(o *elasticloadbalancingv2.Options) { o.BaseEndpoint = aws.String(server.URL) })
	confirmed, err := (awsRegistration{v2: client}).absent(context.Background(), testSettings().Mappings[0])
	if err != nil || !confirmed {
		t.Fatalf("actual AWS SDK transport failed fixture: confirmed=%v error=%v", confirmed, err)
	}
}

func TestNoUnrelatedMutation(t *testing.T) {
	for _, tt := range []struct {
		name string
		body func(string) string
	}{
		{"different target", func(s string) string { return strings.ReplaceAll(s, "i-123", "i-other") }},
		{"port override", func(s string) string { return strings.Replace(s, `"id":"i-123"`, `"id":"i-123","port":8080`, 1) }},
		{"old event", func(s string) string { return strings.Replace(s, "2026-09-13", "2026-08-13", 1) }},
		{"AWS rejection", func(s string) string {
			return strings.Replace(s, `"eventID":"trail-1"`, `"errorCode":"AccessDenied","eventID":"trail-1"`, 1)
		}},
		{"other event", func(s string) string { return strings.Replace(s, "DeregisterTargets", "RegisterTargets", 1) }},
		{"wrong source pair", func(s string) string {
			return strings.Replace(s, `"source":"aws.elasticloadbalancing"`, `"source":"aws.elb"`, 1)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, f, a := newProcessor(t)
			if err := p.process(context.Background(), tt.body(eventBody())); err != nil {
				t.Fatal(err)
			}
			if f.get != 0 || f.patches != 0 || a.calls != 0 {
				t.Fatal("unrelated event caused work")
			}
		})
	}
}

func TestMissingChangedAndReenabledMonitors(t *testing.T) {
	for _, name := range []string{"missing", "recreated", "edited", "reenabled"} {
		t.Run(name, func(t *testing.T) {
			p, f, a := newProcessor(t)
			switch name {
			case "missing":
				f.missing = true
			case "recreated":
				f.monitor.Metadata.UID = "another-incarnation"
			case "edited":
				f.monitor.Metadata.ResourceVersion = "rv-3"
			case "reenabled":
				if err := p.process(context.Background(), eventBody()); err != nil {
					t.Fatal(err)
				}
				f.patches, a.calls = 0, 0
				f.monitor.Spec.Enabled = aws.Bool(true)
				f.monitor.Metadata.ResourceVersion = "rv-3"
			}
			err := p.process(context.Background(), eventBody())
			if name == "missing" && err != nil || name != "missing" && err == nil {
				t.Fatalf("unexpected result: %v", err)
			}
			if f.patches != 0 || a.calls != 0 {
				t.Fatal("changed or missing monitor caused mutation")
			}
		})
	}
}

func TestRegistrationAndConflictAreConservative(t *testing.T) {
	for _, name := range []string{"registered", "draining", "read failure", "conflict"} {
		t.Run(name, func(t *testing.T) {
			p, f, a := newProcessor(t)
			switch name {
			case "registered":
				a.confirmed = false
			case "draining":
				a.err = errDraining
			case "read failure":
				a.err = errors.New("AWS unavailable")
			case "conflict":
				f.status = http.StatusPreconditionFailed
			}
			err := p.process(context.Background(), eventBody())
			if err == nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := 0
			if name == "conflict" {
				want = 1
			}
			if f.patches != want || !*f.monitor.Spec.Enabled {
				t.Fatal("unexpected mutation or retry")
			}
		})
	}
}

type targetHealthFixture struct{ state, reason string }

func (f targetHealthFixture) DescribeTargetHealth(_ context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	return &elasticloadbalancingv2.DescribeTargetHealthOutput{TargetHealthDescriptions: []v2types.TargetHealthDescription{{Target: &in.Targets[0], TargetHealth: &v2types.TargetHealth{State: v2types.TargetHealthStateEnum(f.state), Reason: v2types.TargetHealthReasonEnum(f.reason)}}}}, nil
}

type classicFixture struct{ registered bool }

func (f classicFixture) DescribeLoadBalancers(_ context.Context, in *elasticloadbalancing.DescribeLoadBalancersInput, _ ...func(*elasticloadbalancing.Options)) (*elasticloadbalancing.DescribeLoadBalancersOutput, error) {
	var instances []elbtypes.Instance
	if f.registered {
		instances = []elbtypes.Instance{{InstanceId: aws.String("i-123")}}
	}
	return &elasticloadbalancing.DescribeLoadBalancersOutput{LoadBalancerDescriptions: []elbtypes.LoadBalancerDescription{{LoadBalancerName: aws.String(in.LoadBalancerNames[0]), Instances: instances}}}, nil
}

func TestAWSRegistrationStateMeaning(t *testing.T) {
	for _, tt := range []struct {
		state, reason string
		absent, retry bool
	}{
		{"unused", "Target.NotRegistered", true, false},
		{"unused", "Target.NotInUse", false, false},
		{"unused", "Target.InvalidState", false, false},
		{"unhealthy", "Target.Timeout", false, false},
		{"healthy", "", false, false},
		{"draining", "Target.DeregistrationInProgress", false, true},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			a := awsRegistration{v2: targetHealthFixture{tt.state, tt.reason}}
			absent, err := a.absent(context.Background(), testSettings().Mappings[0])
			if absent != tt.absent || errors.Is(err, errDraining) != tt.retry {
				t.Fatalf("absent=%v error=%v", absent, err)
			}
		})
	}
}

func TestClassicDeregistration(t *testing.T) {
	p, f, _ := newProcessor(t)
	p.cfg.Mappings[0].TargetGroupARN = ""
	p.cfg.Mappings[0].LoadBalancerName = "payments-classic"
	p.aws = awsRegistration{classic: classicFixture{}}
	body := strings.Replace(eventBody(), "DeregisterTargets", "DeregisterInstancesFromLoadBalancer", 1)
	start := strings.Index(body, `"requestParameters"`)
	body = body[:start] + `"requestParameters":{"loadBalancerName":"payments-classic","instances":{"items":[{"instanceId":"i-123"}]}}}}`
	if err := p.process(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if f.patches != 1 {
		t.Fatal("Classic event did not disable mapped monitor")
	}
	a := awsRegistration{classic: classicFixture{registered: true}}
	if absent, err := a.absent(context.Background(), p.cfg.Mappings[0]); absent || !errors.Is(err, errClassicRegistered) {
		t.Fatal("registered Classic instance did not retain its event")
	}
}

func TestInvalidEventsAndMappings(t *testing.T) {
	for _, body := range []string{`{`, eventBody() + `{}`, strings.Repeat("x", maxEventBytes+1), strings.Replace(eventBody(), "123456789012", "999999999999", 1)} {
		p, f, _ := newProcessor(t)
		if err := p.process(context.Background(), body); err == nil || f.patches != 0 {
			t.Fatal("invalid event accepted")
		}
	}
	cfg := testSettings()
	raw, _ := json.Marshal(cfg)
	if _, err := readSettings(strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
	cfg.Mappings = append(cfg.Mappings, cfg.Mappings[0])
	raw, _ = json.Marshal(cfg)
	if _, err := readSettings(strings.NewReader(string(raw))); err == nil {
		t.Fatal("duplicate mapping accepted")
	}
	if _, err := readSettings(strings.NewReader(strings.Repeat(" ", maxEventBytes+1))); err == nil {
		t.Fatal("oversized mapping accepted")
	}
}

type queueFixture struct {
	cancel  context.CancelFunc
	body    string
	deleted bool
	reads   int
}

func (q *queueFixture) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	q.reads++
	if in.WaitTimeSeconds != 20 || in.VisibilityTimeout != 60 || in.MaxNumberOfMessages != 1 {
		return nil, errors.New("incorrect polling bounds")
	}
	if q.reads == 2 {
		q.cancel()
		return nil, context.Canceled
	}
	return &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{{Body: aws.String(q.body), ReceiptHandle: aws.String("receipt")}}}, nil
}

func (q *queueFixture) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	q.deleted = aws.ToString(in.ReceiptHandle) == "receipt"
	return &sqs.DeleteMessageOutput{}, nil
}

func TestQueueAcknowledgesOnlySuccessfulProcessing(t *testing.T) {
	for _, failed := range []bool{false, true} {
		p, _, a := newProcessor(t)
		if failed {
			a.err = errDraining
		}
		ctx, cancel := context.WithCancel(context.Background())
		q := &queueFixture{cancel: cancel, body: eventBody()}
		err := consume(ctx, q, "https://sqs.us-east-1.amazonaws.com/123456789012/events", p, io.Discard)
		cancel()
		if !errors.Is(err, context.Canceled) || q.deleted == failed {
			t.Fatalf("failed=%v deleted=%v error=%v", failed, q.deleted, err)
		}
	}
}

func TestQueueOrigin(t *testing.T) {
	cfg := testSettings()
	if err := validateQueueURL("https://sqs.us-east-1.amazonaws.com/123456789012/events", cfg); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://sqs.us-east-1.amazonaws.com/123456789012/events", "https://other.example/123456789012/events", "https://sqs.us-east-1.amazonaws.com/999999999999/events"} {
		if validateQueueURL(raw, cfg) == nil {
			t.Fatal("unsafe queue origin accepted")
		}
	}
}
