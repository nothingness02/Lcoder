package builtin

// Builtin output limits to avoid flooding the context window / token budget
// when the model reads large files or broad searches.
const (
	// read defaults and hard limits.
	defaultReadLines     = 200
	maxReadFileSizeBytes = 1 << 20 // 1 MiB

	// grep skips files larger than this and caps total result lines.
	maxGrepFileSizeBytes = 1 << 20 // 1 MiB
	maxGrepMatches       = 500

	// find caps the number of returned paths.
	maxFindMatches = 1000

	// ls caps the number of returned directory entries.
	maxLsEntries = 500
)
