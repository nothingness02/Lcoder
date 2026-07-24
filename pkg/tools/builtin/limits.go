package builtin

// Builtin output limits to avoid flooding the context window / token budget
// when the model reads large files or broad searches.
const (
	// read defaults and hard limits. Files larger than maxReadFileSizeBytes
	// require an explicit offset/limit window; files beyond the hard cap are
	// rejected outright (use bash with head/tail/sed instead).
	defaultReadLines         = 200
	maxReadFileSizeBytes     = 1 << 20  // 1 MiB
	maxReadFileSizeHardBytes = 32 << 20 // 32 MiB

	// bash caps combined stdout/stderr; excess is elided head/tail with a
	// marker so a runaway command cannot flood the context.
	maxBashOutputChars = 30000

	// grep skips files larger than this and caps total result lines.
	maxGrepFileSizeBytes = 1 << 20 // 1 MiB
	maxGrepMatches       = 500

	// find caps the number of returned paths.
	maxFindMatches = 1000

	// ls caps the number of returned directory entries.
	maxLsEntries = 500
)
