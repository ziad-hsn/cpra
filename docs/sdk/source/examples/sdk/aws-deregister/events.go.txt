package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const maxEventBytes = 1 << 20

// CloudTrail records a deregistration request, not completion of deregistration.
// Keep these event types separate from CPRa's monitor resource model.
type event struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	DetailType string    `json:"detail-type"`
	Account    string    `json:"account"`
	Region     string    `json:"region"`
	Time       time.Time `json:"time"`
	Detail     struct {
		EventID           string    `json:"eventID"`
		EventSource       string    `json:"eventSource"`
		EventName         string    `json:"eventName"`
		AWSRegion         string    `json:"awsRegion"`
		RecipientAccount  string    `json:"recipientAccountId"`
		EventTime         time.Time `json:"eventTime"`
		ErrorCode         string    `json:"errorCode"`
		ErrorMessage      string    `json:"errorMessage"`
		RequestParameters struct {
			TargetGroupARN   string          `json:"targetGroupArn"`
			LoadBalancerName string          `json:"loadBalancerName"`
			Targets          items[target]   `json:"targets"`
			Instances        items[instance] `json:"instances"`
		} `json:"requestParameters"`
	} `json:"detail"`
}

type target struct {
	ID               string `json:"id"`
	Port             *int32 `json:"port,omitempty"`
	AvailabilityZone string `json:"availabilityZone,omitempty"`
	// Preserve presence, including null, rather than silently discarding an
	// unsupported target identity dimension when decoding newer AWS events.
	QuicServerID json.RawMessage `json:"quicServerId,omitempty"`
}

type instance struct {
	ID string `json:"instanceId"`
}

// CloudTrail list parameters occur as arrays or as objects containing items.
type items[T any] []T

func (v *items[T]) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '[' {
		return json.Unmarshal(raw, (*[]T)(v))
	}
	var wrapper struct {
		Items []T `json:"items"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return err
	}
	if wrapper.Items == nil {
		return errors.New("CloudTrail list must contain items")
	}
	*v = wrapper.Items
	return nil
}

func decodeEvent(body string) (event, error) {
	var e event
	if len(body) > maxEventBytes {
		return e, errors.New("event exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewBufferString(body))
	if err := d.Decode(&e); err != nil {
		return e, errors.New("invalid EventBridge JSON")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return e, errors.New("event contains trailing JSON")
	}
	return e, nil
}

func (e event) isDeregistration() bool {
	sourceOK := e.Source == "aws.elasticloadbalancing" && e.Detail.EventSource == "elasticloadbalancing.amazonaws.com" ||
		e.Source == "aws.elb" && e.Detail.EventSource == "elb.amazonaws.com"
	return sourceOK && e.DetailType == "AWS API Call via CloudTrail" &&
		(e.Detail.EventName == "DeregisterTargets" || e.Detail.EventName == "DeregisterInstancesFromLoadBalancer")
}

func (e event) validate(account, region string) error {
	if e.Account != account || e.Region != region || e.Detail.AWSRegion != region ||
		(e.Detail.RecipientAccount != "" && e.Detail.RecipientAccount != account) {
		return errors.New("event account or region does not match configured owner")
	}
	if e.ID == "" || e.Detail.EventID == "" || e.Detail.EventTime.IsZero() {
		return errors.New("event identity or event time is absent")
	}
	p := e.Detail.RequestParameters
	if e.Detail.EventName == "DeregisterTargets" {
		if p.TargetGroupARN == "" || len(p.Targets) == 0 || len(p.Targets) > 1000 {
			return errors.New("target group or bounded target list is absent")
		}
		for _, t := range p.Targets {
			if len(t.QuicServerID) != 0 {
				return errors.New("QUIC/TCP_QUIC target identity is unsupported; retain event for operator review")
			}
			if t.ID == "" || t.Port != nil && (*t.Port < 1 || *t.Port > 65535) {
				return errors.New("invalid target identity or port")
			}
		}
	} else if p.LoadBalancerName == "" || len(p.Instances) == 0 || len(p.Instances) > 1000 {
		return errors.New("Classic load balancer or bounded instance list is absent")
	} else {
		for _, i := range p.Instances {
			if i.ID == "" {
				return fmt.Errorf("Classic instance identity is absent")
			}
		}
	}
	return nil
}
