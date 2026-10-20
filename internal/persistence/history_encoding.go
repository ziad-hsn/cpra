package persistence

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Older configure commands admitted execution revisions near half the command
// envelope. Keep those exact identities readable without expanding the limits
// for arbitrary event bodies or operation/collection history variants.
func decodeHistoryEvent(raw []byte) (Event, error) {
	var event Event
	if len(raw) == 0 || len(raw) > actionLedgerMaxFrame || json.Unmarshal(raw, &event) != nil {
		return event, ErrHistoryUnavailable
	}
	if len(raw) <= maxHistoryEventBytes {
		return event, nil
	}
	if len(event.Revision) <= 256 || event.Kind == "Collection" || strings.HasPrefix(event.Type, "collection_") ||
		event.Operation != nil || event.Collection != nil ||
		event.CollectionValidation != nil || event.CollectionExecution != nil || event.CollectionExecutionHistory != nil {
		return event, ErrHistoryUnavailable
	}
	for _, prefix := range []string{"collection/", "collection-activation/", "collection-validation/", "collection-execution/"} {
		if strings.HasPrefix(event.MonitorID, prefix) {
			return event, ErrHistoryUnavailable
		}
	}
	canonical, err := json.Marshal(event)
	if err != nil || !bytes.Equal(raw, canonical) {
		return event, ErrHistoryUnavailable
	}
	compact := event
	compact.Revision = ""
	remaining, err := json.Marshal(compact)
	if err != nil || len(remaining) > maxHistoryEventBytes {
		return event, ErrHistoryUnavailable
	}
	return event, nil
}

func encodeHistoryEvent(event Event) ([]byte, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return nil, ErrHistoryUnavailable
	}
	if _, err := decodeHistoryEvent(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
