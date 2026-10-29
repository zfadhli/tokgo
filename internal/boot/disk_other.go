//go:build !unix

package boot

// FreeSpace has no portable answer here, so the header omits the figure.
func FreeSpace(string) (int64, bool) { return 0, false }
