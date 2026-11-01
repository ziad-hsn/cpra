package management

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"slices"
	"strings"
)

func runtimeCredentialBinding(records map[persistence.CatalogKey]persistence.CatalogRecord, drivers ...api.DriverConfig) string {
	ids := map[string]bool{}
	for _, d := range drivers {
		if d.CredentialRefs != nil {
			for _, id := range *d.CredentialRefs {
				ids[id] = true
			}
		}
	}
	bindings := make([]persistence.CatalogCondition, 0, len(ids))
	for id := range ids {
		key := persistence.CatalogKey{Kind: "Credential", ID: id}
		r := records[key]
		bindings = append(bindings, persistence.CatalogCondition{Key: key, UID: r.UID, Revision: r.Revision})
	}
	slices.SortFunc(bindings, func(a, b persistence.CatalogCondition) int { return strings.Compare(a.Key.ID, b.Key.ID) })
	raw, _ := json.Marshal(bindings)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
