// Package memory provides persistent file-based memory storage for Lcoder.
package memory

const (
	// DefaultMemoryCharLimit is the default character cap for the agent memory channel.
	DefaultMemoryCharLimit = 2200
	// DefaultUserCharLimit is the default character cap for the user profile channel.
	DefaultUserCharLimit = 1375
	// EntrySeparator is the line used to split memory entries on disk.
	EntrySeparator = "§"
)
