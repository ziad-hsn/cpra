package persistence

// NotificationFormatVersion records state-change notification semantics in
// observations. Earlier log entries retain their original action identities.
const NotificationFormatVersion = 38

const notificationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-38\n"

func notificationStorageFormat(version int) bool {
	return version == NotificationFormatVersion || deliveryStorageFormat(version) || version > NotificationFormatVersion && externalStorageFormat(version)
}
