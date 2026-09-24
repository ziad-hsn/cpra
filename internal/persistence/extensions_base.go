//go:build !externaljobs

package persistence

// LatestFormatVersion is the highest application storage format supported by
// this build. Optional external state is deliberately unreadable here.
const LatestFormatVersion = CollectionReselectionFormatVersion

type commandExtensions struct{}
type imageExtensions struct{}
type resultExtensions struct{}

func validateCommandExtensions(Command) (bool, error) { return false, nil }
func commandExtensionMinimumFormat(Command) int       { return FormatVersion }
func hasCommandExtensions(Command) bool               { return false }
func externalStorageFormat(int) bool                  { return false }
func externalSnapshotMagic(int) string                { return "" }
func externalSnapshotVersion([]byte) (int, int)       { return 0, 0 }
func cloneImageExtensions(image) imageExtensions      { return imageExtensions{} }
func validateImageExtensions(image) error             { return nil }
func (*machine) applyCommandExtensions(Command, uint64) (Result, bool) {
	return Result{}, false
}

func operationResourceCommand(kind string) bool { return kind == "catalog" }
func validateOperationReservationExtension(OperationReservation) error {
	return ErrOperationReservation
}
func validateOperationAllocationCommand(a OperationAllocation) error { return a.validate() }

func administrativeCommandExtension(Command) bool                             { return false }
func (*machine) validateAuthenticationExtensions(AuthenticationCommand) error { return nil }
func (*machine) resetWorkerPolicy(RestoreMarker) []Event                      { return nil }

type policyExtensions struct{}

func clonePolicyExtensions(Policy) policyExtensions { return policyExtensions{} }
func validatePolicyExtensions(Policy) error         { return nil }

func policyMinimumFormat(Policy) int { return FormatVersion }

func validExternalActionExecutor(image, Action) bool { return false }
