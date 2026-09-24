package persistence

const collectionValidationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-6\n"
const collectionValidationRequestSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-7\n"
const collectionActivationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-8\n"

// Formats 5 onward contain a mandatory plan namespace. Formats 6 and 7 also
// contain the validation-result namespace, including an empty framed stream.
// Format 7 adds only bounded request metadata to the image; its namespace
// streams retain their existing encoding and mandatory order.
func collectionPlanStorageFormat(version int) bool {
	return version == CollectionPlanFormatVersion || collectionValidationStorageFormat(version)
}

func collectionValidationStorageFormat(version int) bool {
	return version == CollectionValidationFormatVersion || collectionValidationRequestStorageFormat(version)
}

func collectionValidationRequestStorageFormat(version int) bool {
	return version == CollectionValidationRequestFormatVersion || collectionActivationStorageFormat(version)
}
