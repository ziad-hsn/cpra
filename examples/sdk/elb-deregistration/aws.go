package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	v2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type awsClients struct {
	identity     *sts.Client
	registration awsRegistration
	queue        *sqs.Client
}

// Resolve every service's options before requesting credentials or identity.
// LoadDefaultConfig also accepts global and per-service endpoint overrides;
// those would invalidate the configured mode's claim to query AWS itself.
func newAWSClients(cfg aws.Config) (awsClients, error) {
	if cfg.EndpointResolver != nil || cfg.EndpointResolverWithOptions != nil {
		return awsClients{}, errors.New("custom AWS endpoint resolvers are unsupported in configured mode")
	}
	identity := sts.NewFromConfig(cfg)
	v2 := elasticloadbalancingv2.NewFromConfig(cfg)
	classic := elasticloadbalancing.NewFromConfig(cfg)
	queue := sqs.NewFromConfig(cfg)
	for _, endpoint := range []*string{identity.Options().BaseEndpoint, v2.Options().BaseEndpoint, classic.Options().BaseEndpoint, queue.Options().BaseEndpoint} {
		if endpoint != nil && *endpoint != "" {
			return awsClients{}, errors.New("custom AWS service endpoints are unsupported in configured mode; use explicit local fixtures")
		}
	}
	return awsClients{identity: identity, registration: awsRegistration{v2: v2, classic: classic}, queue: queue}, nil
}

type targetHealthAPI interface {
	DescribeTargetHealth(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

type classicAPI interface {
	DescribeLoadBalancers(context.Context, *elasticloadbalancing.DescribeLoadBalancersInput, ...func(*elasticloadbalancing.Options)) (*elasticloadbalancing.DescribeLoadBalancersOutput, error)
}

type awsRegistration struct {
	v2      targetHealthAPI
	classic classicAPI
}

var errDraining = errors.New("target deregistration is still draining; retain event for retry")
var errClassicRegistered = errors.New("Classic instance remains registered; retain event for absence confirmation or DLQ review")

func (r awsRegistration) absent(ctx context.Context, m mapping) (bool, error) {
	if m.TargetGroupARN != "" {
		target := v2types.TargetDescription{Id: aws.String(m.TargetID), Port: m.Port}
		if m.AvailabilityZone != "" {
			target.AvailabilityZone = aws.String(m.AvailabilityZone)
		}
		out, err := r.v2.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(m.TargetGroupARN), Targets: []v2types.TargetDescription{target},
		})
		if err != nil {
			return false, err
		}
		if out == nil || len(out.TargetHealthDescriptions) != 1 {
			return false, errors.New("AWS returned no unambiguous target registration evidence")
		}
		d := out.TargetHealthDescriptions[0]
		if d.Target == nil || aws.ToString(d.Target.Id) != m.TargetID || d.TargetHealth == nil ||
			m.Port != nil && (d.Target.Port == nil || *d.Target.Port != *m.Port) ||
			m.AvailabilityZone != "" && aws.ToString(d.Target.AvailabilityZone) != m.AvailabilityZone {
			return false, errors.New("AWS target evidence does not match requested target")
		}
		if d.TargetHealth.State == v2types.TargetHealthStateEnumDraining && d.TargetHealth.Reason == v2types.TargetHealthReasonEnumDeregistrationInProgress {
			return false, errDraining
		}
		return d.TargetHealth.State == v2types.TargetHealthStateEnumUnused && d.TargetHealth.Reason == v2types.TargetHealthReasonEnumNotRegistered, nil
	}
	out, err := r.classic.DescribeLoadBalancers(ctx, &elasticloadbalancing.DescribeLoadBalancersInput{
		LoadBalancerNames: []string{m.LoadBalancerName},
	})
	if err != nil {
		return false, err
	}
	if out == nil || len(out.LoadBalancerDescriptions) != 1 || aws.ToString(out.LoadBalancerDescriptions[0].LoadBalancerName) != m.LoadBalancerName {
		return false, errors.New("AWS returned no unambiguous Classic load balancer evidence")
	}
	if containsInstance(out.LoadBalancerDescriptions[0].Instances, m.TargetID) {
		// Classic connection draining can leave a deregistering instance listed.
		// Keep the event until a later read establishes absence.
		return false, errClassicRegistered
	}
	return true, nil
}

func containsInstance(instances []elbtypes.Instance, id string) bool {
	for _, i := range instances {
		if aws.ToString(i.InstanceId) == id {
			return true
		}
	}
	return false
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// One event at a time gives every delivery a bounded processing budget shorter
// than its visibility timeout. Configure SQS redrive for persistent failures.
func consume(ctx context.Context, queue queueAPI, url string, p *processor, output io.Writer) error {
	for ctx.Err() == nil {
		pollCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		out, err := queue.ReceiveMessage(pollCtx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(url), MaxNumberOfMessages: 1, WaitTimeSeconds: 20, VisibilityTimeout: 60,
		})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("receive AWS event: %w", err)
		}
		if out == nil {
			return errors.New("SQS returned an empty response object")
		}
		for _, message := range out.Messages {
			workCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			err := p.process(workCtx, aws.ToString(message.Body))
			cancel()
			if err != nil {
				fmt.Fprintf(output, "event retained for retry or DLQ: %v\n", err)
				continue
			}
			ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err = queue.DeleteMessage(ackCtx, &sqs.DeleteMessageInput{QueueUrl: aws.String(url), ReceiptHandle: message.ReceiptHandle})
			cancel()
			if err != nil {
				return fmt.Errorf("delete processed event: %w", err)
			}
		}
	}
	return ctx.Err()
}

// fixtureRegistration is selected only by an explicit flag, never by failed AWS authentication.
type fixtureRegistration struct{}

func (fixtureRegistration) absent(context.Context, mapping) (bool, error) { return true, nil }
