package version

// Version is the fallback for unstampeded go run builds. Release builds inject
// the repository-root VERSION via -ldflags; CI verifies that tag == VERSION.
var Version = "dev"

const ProductName = "TR Driver"
