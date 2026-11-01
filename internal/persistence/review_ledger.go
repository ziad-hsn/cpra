package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/btree"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// Assessment destinations are fixed-size identities, never arbitrary input IDs.
const reviewLedgerMaxFrame = actionLedgerMaxFrame

var reviewRowsBucket = []byte("action_review_records")
var reviewReadyBucket = []byte("action_review_ready")

// reviewFact captures a committed observation, not a new authorization. Fence
// names the post-command action version. Progress is conditional and survives
// both replay and snapshot restoration.
type reviewDestination struct {
	ID      string        `json:"id"`
	Timeout time.Duration `json:"timeout"`
	Binding string        `json:"binding,omitempty"`
}

type reviewFact struct {
	IndexVersion int       `json:"index_version,omitempty"`
	PolicyID     string    `json:"policy_id"`
	ID           string    `json:"id"`
	RestoreID    string    `json:"restore_id,omitempty"`
	At           time.Time `json:"at"`
	Fence        uint64    `json:"fence"`
	Context      Monitor   `json:"context"`
	SourceAction string    `json:"source_action,omitempty"`
	Phase        string    `json:"phase"`
	After        string    `json:"after,omitempty"`
	Endpoint     int       `json:"endpoint,omitempty"`
	Revision     uint64    `json:"revision"`
}

type reviewRecord struct {
	Key       string            `json:"key"`
	MonitorID string            `json:"monitor_id"`
	Sequence  uint64            `json:"sequence"`
	Fact      *reviewFact       `json:"fact,omitempty"`
	Alert     *ReviewAlert      `json:"alert,omitempty"`
	Policy    *reviewPolicyPage `json:"policy,omitempty"`
	Deleted   bool              `json:"deleted,omitempty"`
}

func reviewPrefix(kind, monitor string) string { return kind + "/" + identity(monitor) + "/" }
func reviewAlertKey(monitor, id string) string { return reviewPrefix("alert", monitor) + id }

func encodeReviewRecord(r reviewRecord) ([]byte, error) {
	if r.Sequence == 0 || !catalogIdentifier(r.MonitorID, 256) || len(r.Key) > 512 || !strings.HasPrefix(r.Key, reviewPrefix("fact", r.MonitorID)) && !strings.HasPrefix(r.Key, reviewPrefix("alert", r.MonitorID)) && !strings.HasPrefix(r.Key, reviewPrefix("policy", r.MonitorID)) {
		return nil, errActionLedger
	}
	if r.Deleted {
		if r.Fact != nil || r.Alert != nil || r.Policy != nil {
			return nil, errActionLedger
		}
	} else {
		variants := 0
		for _, present := range []bool{r.Fact != nil, r.Alert != nil, r.Policy != nil} {
			if present {
				variants++
			}
		}
		if variants != 1 {
			return nil, errActionLedger
		}
	}
	if p := r.Policy; p != nil {
		if !bootstrapHash(p.ID) || p.Count < 0 || p.Count > math.MaxInt32 || p.Offset < -1 || p.Offset >= p.Count && p.Offset != -1 || p.Offset >= 0 && p.Offset%reviewPolicyPageSize != 0 || r.Key != reviewPolicyKey(r.MonitorID, p.ID, p.Offset) {
			return nil, errActionLedger
		}
		if p.Offset == -1 && p.Revision == "" || p.Offset != -1 && p.Revision != "" {
			return nil, errActionLedger
		}
		expected := 0
		if p.Offset >= 0 {
			if !p.Explicit {
				return nil, errActionLedger
			}
			expected = min(reviewPolicyPageSize, p.Count-p.Offset)
		}
		if len(p.Destinations) != expected {
			return nil, errActionLedger
		}
		for _, d := range p.Destinations {
			if !bootstrapHash(d.ID) || d.Timeout <= 0 || d.Timeout > 24*time.Hour || d.Binding != "" && !bootstrapHash(d.Binding) {
				return nil, errActionLedger
			}
		}
	}
	if f := r.Fact; f != nil {
		if f.IndexVersion < 0 || f.IndexVersion > 1 || !bootstrapHash(f.PolicyID) || validateReviewContext(f.Context, f.At) != nil {
			return nil, errActionLedger
		}
		if f.RestoreID != "" {
			id, err := uuid.Parse(f.RestoreID)
			if err != nil || id == uuid.Nil || id.String() != f.RestoreID {
				return nil, errActionLedger
			}
		}
		prefix := reviewPrefix("fact", r.MonitorID)
		var index uint64
		var ordinal uint32
		suffix := strings.TrimPrefix(r.Key, prefix)
		n, e := fmt.Sscanf(suffix, "%d-%d", &index, &ordinal)
		if !strings.HasPrefix(r.Key, prefix) || e != nil || n != 2 || index == 0 || suffix != fmt.Sprintf("%020d-%010d", index, ordinal) || f.SourceAction != "" && !catalogIdentifier(f.SourceAction, 256) || f.At.Year() < 1 || f.At.Year() > 9999 ||
			f.Phase == "actions" && f.After != "" && !catalogIdentifier(f.After, 256) || f.Phase == "conditions" && (f.Endpoint != 0 || f.After != "" && (!strings.HasPrefix(f.After, reviewPrefix("alert", r.MonitorID)) || len(f.After) > 512)) || f.Endpoint > f.Context.Policy.Endpoints["blue"] {
			return nil, errActionLedger
		}

		if f.ID != r.Key || f.Context.ID != r.MonitorID || f.At.IsZero() || f.Fence == 0 || f.Fence > r.Sequence || f.Revision == 0 || (f.Phase != "actions" && f.Phase != "conditions") || f.Endpoint < 0 {
			return nil, errActionLedger
		}
	}
	if r.Alert != nil && (r.Key != reviewAlertKey(r.MonitorID, r.Alert.ID) || validateReviewAlertFields(*r.Alert) != nil) {
		return nil, errActionLedger
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > reviewLedgerMaxFrame {
		return nil, errActionLedger
	}
	return data, nil
}

func validateReviewContext(m Monitor, at time.Time) error {
	if !catalogIdentifier(m.ID, 256) || m.Revision != "" || !bootstrapHash(m.ReviewRevisionDigest) || m.Policy.Validate() != nil || !reflect.DeepEqual(m, reviewContext(m, at)) {
		return errActionLedger
	}
	for _, id := range []string{m.CatalogUID, m.FileIncarnation, m.IncidentID, m.IncidentRevision, m.NotificationEpisode} {
		if id != "" && !catalogIdentifier(id, 256) {
			return errActionLedger
		}
	}
	if m.InstallationID != "" && !catalogIdentifier(m.InstallationID, 256) {
		return errActionLedger
	}
	if m.AdoptedFileIncarnation != nil && (m.CatalogUID == "" || *m.AdoptedFileIncarnation != m.FileIncarnation) {
		return errActionLedger
	}
	if m.VerifyRemaining < 0 || m.Incident && (m.IncidentID == "" || m.IncidentOpenedAt.IsZero() || !m.IncidentClosedAt.IsZero()) || !m.IncidentOpenedAt.IsZero() && m.IncidentID == "" || !m.IncidentClosedAt.IsZero() && m.IncidentID == "" {
		return errActionLedger
	}
	if m.Notifications != nil {
		if m.Notifications.Version != 0 && m.Notifications.Version != NotificationConditionFormatVersion {
			return errActionLedger
		}
		if m.Notifications.QualifiedIncident != "" && m.Notifications.QualifiedIncident != m.IncidentID {
			return errActionLedger
		}
	}
	if m.ConfigurationProblem != (FailureDiagnostic{}) {
		if !m.notificationConditionsEnabled() || !m.ConfigurationProblem.configurationProblem() ||
			m.ConfigurationProblem.validate(NotificationConditionFormatVersion) != nil || m.ConfigurationProblemAt.IsZero() {
			return errActionLedger
		}
	} else if !m.ConfigurationProblemAt.IsZero() {
		return errActionLedger
	}
	return nil
}
func decodeReviewRecord(data []byte) (reviewRecord, error) {
	var r reviewRecord
	if len(data) == 0 || len(data) > reviewLedgerMaxFrame {
		return r, errActionLedger
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return r, errActionLedger
	}
	canonical, err := encodeReviewRecord(r)
	if err != nil || !bytes.Equal(canonical, data) {
		return r, errActionLedger
	}
	return r, nil
}

func encodeReviewUpdate(previous []byte, r reviewRecord) ([]byte, error) {
	if len(previous) != 0 {
		old, err := decodeReviewRecord(previous)
		if err != nil {
			return nil, err
		}
		if old.Policy != nil {
			// Exact-position replay was handled before writes. A new position
			// cannot rewrite even identical immutable policy bytes: doing so
			// would detach their creation fence from the commit digest.
			return nil, errActionLedger
		}
	}
	return encodeReviewRecord(r)
}
func (d *actionCommandDigest) addReview(r reviewRecord) error {
	r.Sequence = 0
	data, err := json.Marshal(r)
	if err != nil {
		return errActionLedger
	}
	if d.count != 0 {
		_, _ = d.hash.Write([]byte(","))
	}
	_, _ = d.hash.Write(data)
	d.count++
	return nil
}
func putReviewRecord(tx *bolt.Tx, r reviewRecord, data []byte) error {
	if err := updateReviewIndexes(tx, r); err != nil {
		return err
	}
	if err := tx.Bucket(reviewRowsBucket).Put([]byte(r.Key), data); err != nil {
		return err
	}
	if r.Deleted {
		return tx.Bucket(reviewReadyBucket).Delete([]byte(r.Key))
	}
	return tx.Bucket(reviewReadyBucket).Put([]byte(r.Key), []byte(r.MonitorID))
}
func (l *collectionLedger) reviewRecord(key string) (reviewRecord, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return reviewRecord{}, false, errCollectionLedgerClosed
	}
	var data []byte
	var sequence uint64
	if l.db != nil {
		if err := l.db.View(func(tx *bolt.Tx) error {
			switch actionBucketCount(tx) {
			case 0:
				return nil // A collection-only generation has no review records yet.
			case len(actionBuckets()):
			default:
				return errActionLedger
			}
			progress, err := actionDiskProgress(tx)
			if err != nil {
				return err
			}
			sequence = progress.Sequence
			if b := tx.Bucket(reviewRowsBucket); b != nil {
				data = bytes.Clone(b.Get([]byte(key)))
			}
			return nil
		}); err != nil {
			return reviewRecord{}, false, err
		}
	} else if l.actions != nil {
		data = l.actions.reviews[key]
		sequence = l.actions.progress.Sequence
	}
	if len(data) == 0 {
		return reviewRecord{}, false, nil
	}
	r, err := decodeReviewRecord(data)
	if err == nil && (r.Key != key || r.Sequence > sequence) {
		return reviewRecord{}, false, errActionLedger
	}
	return r, !r.Deleted && err == nil, err
}

// reviewPage uses a live ordered index. Facts are processed in committed order;
// conditions are deliberately read at their latest state, preserving exposure
// recorded by starts after an earlier assessment fact.
func (l *collectionLedger) reviewPage(prefix, after string, limit int) ([]reviewRecord, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed || limit < 1 || limit > 64 {
		return nil, errActionLedger
	}
	var rows []reviewRecord
	visit := func(key string, data []byte) (bool, error) {
		if !strings.HasPrefix(key, prefix) {
			return false, nil
		}
		if key <= after {
			return true, nil
		}
		r, err := decodeReviewRecord(data)
		if err != nil || r.Deleted || r.Key != key {
			return false, errActionLedger
		}
		rows = append(rows, r)
		return len(rows) < limit, nil
	}
	if l.db != nil {
		err := l.db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket(reviewReadyBucket)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			start := max(prefix, after)
			for k, _ := c.Seek([]byte(start)); k != nil; k, _ = c.Next() {
				more, err := visit(string(k), tx.Bucket(reviewRowsBucket).Get(k))
				if err != nil {
					return err
				}
				if !more {
					break
				}
			}
			return nil
		})
		return rows, err
	}
	if l.actions == nil {
		return rows, nil
	}
	var err error
	l.actions.reviewOrder.AscendGreaterOrEqual(max(prefix, after), func(k string) bool {
		var more bool
		more, err = visit(k, l.actions.reviews[k])
		return more && err == nil
	})
	return rows, err
}
func (v *collectionLedgerView) walkReviews(visit func([]byte, reviewRecord) error) error {
	read := func(k string, data []byte) error {
		r, err := decodeReviewRecord(data)
		if err != nil || r.Key != k {
			return errActionLedger
		}
		return visit(data, r)
	}
	if v.tx != nil {
		if b := v.tx.Bucket(reviewRowsBucket); b != nil {
			return b.ForEach(func(k, d []byte) error { return read(string(k), d) })
		}
		return nil
	}
	if v.actions == nil {
		return nil
	}
	keys := make([]string, 0, len(v.actions.reviews))
	for k := range v.actions.reviews {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if err := read(k, v.actions.reviews[k]); err != nil {
			return err
		}
	}
	return nil
}
func newReviewOrder() *btree.BTreeG[string] {
	return btree.NewG(32, func(a, b string) bool { return a < b })
}
