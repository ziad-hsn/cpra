//go:build !externaljobs

package persistence

import "encoding/json"

type catalogRecordExtensions struct{}

func cloneCatalogRecordExtensions(CatalogRecord) catalogRecordExtensions {
	return catalogRecordExtensions{}
}
func validateCatalogRecordExtensions(CatalogRecord) error                              { return nil }
func catalogRecordMinimumFormat(CatalogRecord) int                                     { return FormatVersion }
func validateCatalogJobTypeReferences(image, CatalogRecord, bool) error                { return nil }
func validateCatalogRecordImageExtensions(image, CatalogRecord, bool) error            { return nil }
func addCatalogRecordExtensions(map[string]map[string]struct{}, CatalogRecord)         {}
func (*machine) installCatalogRecordExtensions(catalogRecordExtensions, CatalogRecord) {}
func operationDigestEncoding(c Command) ([]byte, error) {
	return json.Marshal(legacyOperationCommandDigest(c))
}
