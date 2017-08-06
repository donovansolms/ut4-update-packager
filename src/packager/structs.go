package packager

const (
	deltaOperationAdded    = "added"
	deltaOperationModified = "modified"
	deltaOperationRemoved  = "removed"
)

// UT4Modules is the structure of the .modules file
type UT4Modules struct {
	Changelist           int
	CompatibleChangelist int
	BuildID              string
}
