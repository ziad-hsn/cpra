package management

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCatalogAndCollectionKeepValidationCause(t *testing.T) {
	catalog, _ := testCatalog(t)
	input := resource("Monitor", "missing-url", json.RawMessage(`{"check":{"interval":"1s","timeout":"1s","driver":{"type":"http","config":{}}}}`))
	_, err := catalog.Prepare(t.Context(), input, "", true)
	field, ok := api.ValidationFieldError(err)
	if !errors.Is(err, ErrValidation) || !ok || field.Field != "spec.check.driver.config.url" || field.Reason != "required" {
		t.Fatal("direct mutation erased cause", err)
	}
	view, err := catalog.SnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, _ := catalog.ValidateCollection(t.Context(), view, []CollectionInput{{Resource: input, SourceID: "source.00000000000000000001", ItemID: "item.00000000000000000001"}}, collectionReadAll)
	if result.Valid || len(result.Items) != 1 || result.Items[0].Field != field.Field || result.Items[0].Reason != field.Reason {
		t.Fatal("preflight erased cause", result.Items)
	}
	f := stagedValidationFixture(t, input)
	verdict, _, compileErr, source := validationArtifactCompile(t, &f)
	artifact, err := prepareCollectionValidationArtifact(t.Context(), source, verdict, compileErr)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.close()
	items := validationArtifactItems(t, artifact)
	if len(items) != 1 || items[0].Field != field.Field || items[0].Reason != field.Reason {
		t.Fatal("retained artifact erased cause", items)
	}
}
