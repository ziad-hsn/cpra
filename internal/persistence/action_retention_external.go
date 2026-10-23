//go:build externaljobs

package persistence

func workerRetainsAction(i image, id string) bool {
	if i.WorkerExecutions == nil {
		return false
	}
	for _, record := range i.WorkerExecutions.Records {
		if record.Intent.ActionID == id && workerActionDependencyRequired(record) {
			return true
		}
	}
	return false
}
