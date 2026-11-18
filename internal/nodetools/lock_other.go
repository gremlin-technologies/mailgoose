//go:build !unix

package nodetools

// lock does nothing where flock doesn't exist; run `npm ci` in web/ first
// there.
func lock(string) (func(), error) { return func() {}, nil }
