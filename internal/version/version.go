// Package version holds the application version used by the UI, the update
// checker and the release uploader.
package version

// Version is the semantic version of this build.
const Version = "1.3.20"

// String returns the version without a leading "v".
func String() string { return Version }









