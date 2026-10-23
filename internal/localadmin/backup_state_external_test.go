//go:build externaljobs

package localadmin

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestStoppedBackupRecordsCommittedWorkerStateFormat(t *testing.T) {
	testStoppedBackupStateFormat(t, persistence.WorkerNotificationConditionFormatVersion, func(t *testing.T, store *persistence.Store) {
		at := time.Now().UTC()
		authority, err := store.ObserveOperatorAuthority(t.Context(), "oncall", at)
		if err != nil {
			t.Fatal(err)
		}
		wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{42}, 32))
		if err != nil {
			t.Fatal(err)
		}
		sealer, err := secureconfig.NewSealer(wrapper)
		if err != nil {
			t.Fatal(err)
		}
		record := persistence.CatalogRecord{
			Key: persistence.CatalogKey{Kind: "JobType", ID: "backup-check"},
			UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1,
			Purpose: "job-type", CreatedAt: at, UpdatedAt: at,
		}
		record.Payload, err = sealer.Seal(t.Context(), record.Binding(store.Status().NodeID), []byte("fixture-job-type-schema"))
		if err != nil {
			t.Fatal(err)
		}
		// Registering a descriptor commits optional state. This fixture starts
		// no worker or handler and performs no provider operations.
		_, err = store.CommitJobType(t.Context(), persistence.JobTypeCommand{
			Action: "create", Authority: authority,
			Value: persistence.JobTypeVersion{Record: record, Version: "v1", Category: "check",
				Handler: "probe", ProtocolVersion: "1", SchemaProfile: "cpra.schema.v1"},
		}, at)
		if err != nil {
			t.Fatal(err)
		}
	})
}
