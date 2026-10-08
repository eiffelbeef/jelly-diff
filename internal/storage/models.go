package storage

import "time"

// Snapshot represents a point-in-time inventory of a library.
type Snapshot struct {
	ID          int64
	TakenAt     time.Time
	LibraryID   string
	LibraryName string
	ItemCount   int
}

// Item is the latest known state of a Jellyfin media item.
type Item struct {
	JellyfinID string
	LibraryID  string
	Title      string
	SortTitle  string
	MediaType  string
	Year       int
	ImageTag   string
	AddedAt    time.Time
	LastSeenAt time.Time
	IsRemoved  bool
}

// Event is an immutable add/remove record.
type Event struct {
	ID          int64
	SnapshotID  int64
	JellyfinID  string
	EventType   string // "added" | "removed"
	OccurredAt  time.Time
	Title       string
	LibraryName string
	MediaType   string
	ImageTag    string
}

// EventFilter holds optional query parameters for listing events.
type EventFilter struct {
	Library          string
	EventType        string
	From             time.Time
	To               time.Time
	Query            string
	AllowedLibraries []string // Restricts events to these libraries (if non-nil)
	Page             int
	Limit            int
}

// Stats holds the summary data shown on the dashboard.
type Stats struct {
	TotalItems       int
	AddedThisWeek    int
	AddedThisMonth   int
	RemovedThisWeek  int
	RemovedThisMonth int
	LastSyncAt       *time.Time
	DailyChart       []DayCount
}

// DayCount holds per-day add/remove totals for the bar chart.
type DayCount struct {
	Date    string
	Added   int
	Removed int
}

// SessionRecord represents a persisted user session row in SQLite.
type SessionRecord struct {
	ID        string
	UserID    string
	Username  string
	Token     string
	Libraries []string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// MetadataUpdate holds changes for denormalised event title and image.
type MetadataUpdate struct {
	EventID  string
	Title    string
	ImageTag string
}

// EventUpgrade holds re-linking information for upgraded media items.
type EventUpgrade struct {
	OldID       string
	NewTargetID string
	NewImageTag string
}

// LibrarySyncData holds all mutations to persist for a library in a single atomic transaction.
type LibrarySyncData struct {
	Snapshot        Snapshot
	Items           []Item
	MetadataUpdates []MetadataUpdate
	EventUpgrades   []EventUpgrade
	UpgradedOldIDs  []string
	AddedEvents     []Event
	RemovedEvents   []Event
	RemovedIDs      []string
}

