package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (c *CodeOpsgenieJob) deliverOpsgenie(ctx context.Context) error {
	base, err := notificationURL(c.URL, "https://api.opsgenie.com/v2/alerts")
	if err != nil {
		return err
	}
	alias := c.CorrelationKey
	if alias == "" {
		alias = "cpra:" + c.ID.String()
	}
	priority := "P5"
	if c.Color == "red" {
		priority = "P1"
	} else if c.Color == "yellow" {
		priority = "P3"
	}
	data := map[string]string{"message": truncateNotification(c.notificationMessage(c.Message), 130), "description": truncateNotification(c.notificationMessage(c.Message), 15000), "alias": alias, "priority": priority}
	target := base
	if c.Color == "green" {
		if c.CorrelationKey == "" {
			return &DeliveryError{Status: http.StatusBadRequest}
		}
		parsed, err := url.Parse(base)
		if err != nil {
			return fmt.Errorf("Opsgenie close URL is invalid")
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + alias + "/close"
		query := parsed.Query()
		query.Set("identifierType", "alias")
		parsed.RawQuery = query.Encode()
		target = parsed.String()
		data = map[string]string{"source": "CPRa", "note": truncateNotification(c.notificationMessage(c.Message), 15000)}
	}
	status, raw, err := c.opsgenieRequest(ctx, http.MethodPost, target, data)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted {
		return fmt.Errorf("Opsgenie did not provide asynchronous acceptance evidence")
	}
	var receipt struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return fmt.Errorf("Opsgenie acceptance receipt is invalid")
	}
	id, err := uuid.Parse(receipt.RequestID)
	if err != nil || id == uuid.Nil || id.String() != receipt.RequestID {
		return fmt.Errorf("Opsgenie acceptance lacks a valid request identity")
	}
	parsed, _ := url.Parse(base)
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/requests/" + receipt.RequestID
	for {
		status, raw, err := c.opsgenieRequest(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Acceptance has already happened. A status-read rejection must
			// never authorize repetition of the original create/close request.
			return fmt.Errorf("Opsgenie processing outcome is unknown")
		}
		var result struct {
			Data struct {
				Success   *bool `json:"success"`
				IsSuccess *bool `json:"isSuccess"`
			} `json:"data"`
		}
		if status == http.StatusOK {
			if json.Unmarshal(raw, &result) != nil {
				return fmt.Errorf("Opsgenie processing receipt is invalid")
			}
			if result.Data.Success != nil && result.Data.IsSuccess != nil {
				if *result.Data.Success && *result.Data.IsSuccess {
					return nil
				}
				return fmt.Errorf("Opsgenie processing did not confirm a successful effect")
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func (c *CodeOpsgenieJob) opsgenieRequest(ctx context.Context, method, target string, data map[string]string) (int, []byte, error) {
	var body []byte
	if data != nil {
		body, _ = json.Marshal(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "GenieKey "+c.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := effectHTTPClient(10 * time.Second).Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	if err := notificationStatusError(response.StatusCode); err != nil {
		return response.StatusCode, nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return response.StatusCode, nil, fmt.Errorf("Opsgenie receipt could not be read within its limit")
	}
	return response.StatusCode, raw, nil
}
