package policy

import "regexp"

// runIDPattern is the accepted shape of a run id. It must begin with an
// alphanumeric character and then allow up to 117 more of the same plus dot,
// underscore, and dash, for 118 characters total. The leading-character rule
// keeps ids from looking like flags or dotfiles, and the restricted set keeps a
// run id safe to use as a database key, a log field (no newlines to inject
// with), and a path component (no slashes or traversal).
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,117}$`)

// RunIDRule is a human-readable statement of the run id rule, for error
// messages that reject a malformed id.
const RunIDRule = "a run id must start with a letter or digit and use only " +
	"letters, digits, '.', '_', and '-', up to 118 characters"

// ValidRunID reports whether id is a well-formed run id. It is applied wherever
// a run id enters leash from outside: the X-Loop-Id request header and the
// --run flag. Rejecting malformed ids at the door prevents log injection via
// header newlines and stops a caller from steering the ledger key.
func ValidRunID(id string) bool {
	return runIDPattern.MatchString(id)
}
