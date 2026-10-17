package affected

// MainLabels and HashedLabels are the join's label readers, for the tests
// that run Bazel outside the package.
var (
	MainLabels   = mainLabelList
	HashedLabels = readHashedLabels
)
