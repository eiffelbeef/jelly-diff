package sync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/eiffelbeef/jelly-diff/internal/config"
	"github.com/eiffelbeef/jelly-diff/internal/jellyfin"
	"github.com/eiffelbeef/jelly-diff/internal/notify"
	"github.com/eiffelbeef/jelly-diff/internal/storage"
)

// TriggerResult indicates the outcome of a manual sync trigger attempt.
type TriggerResult int

const (
	// TriggerAccepted means a sync was successfully scheduled.
	TriggerAccepted TriggerResult = iota
	// TriggerAlreadyRunning means a sync is currently in progress.
	TriggerAlreadyRunning
	// TriggerCooldown means the minimum interval since the last sync has not elapsed.
	TriggerCooldown
	// TriggerUnavailable means the worker is not running or stopped.
	TriggerUnavailable
)

// Worker orchestrates periodic + manual Jellyfin sync cycles.
type Worker struct {
	cfg              *config.Config
	client           *jellyfin.Client
	store            *storage.Store
	notifier         notify.Notifier
	trigger          chan struct{}
	mu               sync.Mutex
	isSyncing        bool
	running          bool
	lastSyncFinished time.Time
	lastSyncEvents   int
	syncSeq          int64
}

// NewWorker creates a Worker. Pass a nil notifier to disable notifications.
func NewWorker(cfg *config.Config, client *jellyfin.Client, store *storage.Store, n notify.Notifier) *Worker {
	w := &Worker{
		cfg:      cfg,
		client:   client,
		store:    store,
		notifier: n,
		trigger:  make(chan struct{}, 1),
		syncSeq:  1,
	}
	if store != nil {
		if lastSync, err := store.GetLastSyncAt(context.Background()); err == nil && lastSync != nil {
			w.lastSyncFinished = *lastSync
		}
	}
	return w
}

// Trigger requests an immediate out-of-band sync without returning status.
func (w *Worker) Trigger() {
	_, _ = w.TriggerSync()
}

// TriggerSync attempts to initiate an immediate sync.
// It returns TriggerAccepted if scheduled, TriggerAlreadyRunning if a sync is running,
// TriggerCooldown (with the remaining duration) if on cooldown, or TriggerUnavailable if the worker is stopped.
func (w *Worker) TriggerSync() (TriggerResult, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.running {
		return TriggerUnavailable, 0
	}

	if w.isSyncing {
		return TriggerAlreadyRunning, 0
	}

	if w.cfg != nil && w.cfg.MinSyncInterval > 0 && !w.lastSyncFinished.IsZero() {
		elapsed := time.Since(w.lastSyncFinished)
		if elapsed < w.cfg.MinSyncInterval {
			remaining := w.cfg.MinSyncInterval - elapsed
			return TriggerCooldown, remaining
		}
	}

	w.isSyncing = true
	select {
	case w.trigger <- struct{}{}:
	default:
	}

	return TriggerAccepted, 0
}

// IsSyncing returns whether a sync is currently running.
func (w *Worker) IsSyncing() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.isSyncing
}

// LastSyncFinished returns the timestamp when the last sync finished.
func (w *Worker) LastSyncFinished() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastSyncFinished
}

// SetLastSyncFinished manually sets the last sync finished time (useful in tests).
func (w *Worker) SetLastSyncFinished(t time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastSyncFinished = t
}

// SetRunning marks the worker as running (useful in tests when Start is not run in background).
func (w *Worker) SetRunning(running bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running = running
}

// SetSyncing sets the syncing status (useful in tests).
func (w *Worker) SetSyncing(syncing bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.isSyncing = syncing
}

// SyncSeq returns the current sync sequence counter.
func (w *Worker) SyncSeq() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.syncSeq
}

// SetSyncSeq sets the sync sequence counter (useful in tests).
func (w *Worker) SetSyncSeq(seq int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.syncSeq = seq
}

// LastSyncEvents returns the number of events created in the last sync.
func (w *Worker) LastSyncEvents() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastSyncEvents
}

// SetLastSyncEvents sets the number of events created in the last sync (useful in tests).
func (w *Worker) SetLastSyncEvents(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastSyncEvents = n
}

// Start runs the sync loop until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	log := slog.With("component", "sync-worker")
	interval := time.Hour
	if w.cfg != nil && w.cfg.SyncInterval > 0 {
		interval = w.cfg.SyncInterval
	}
	log.Info("starting sync worker", "interval", interval)

	w.mu.Lock()
	w.running = true
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.running = false
		w.isSyncing = false
		w.mu.Unlock()
	}()

	w.runSync(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("sync worker stopping")
			return
		case <-ticker.C:
			w.runSync(ctx)
		case <-w.trigger:
			log.Info("manual sync triggered")
			w.runSync(ctx)
			ticker.Reset(interval)
		}
	}
}

func (w *Worker) runSync(ctx context.Context) {
	log := slog.With("component", "sync-worker")
	start := time.Now()
	log.Info("sync started")

	w.mu.Lock()
	w.isSyncing = true
	w.mu.Unlock()

	var allEvents []storage.Event
	syncSuccess := false

	defer func() {
		w.mu.Lock()
		w.isSyncing = false
		if syncSuccess {
			w.lastSyncFinished = time.Now()
			w.lastSyncEvents = len(allEvents)
			w.syncSeq++
		}
		w.mu.Unlock()
	}()

	libs, err := w.client.GetLibraries(ctx)
	if err != nil {
		log.Error("get libraries failed", "err", err)
		return
	}
	log.Info("fetched libraries", "count", len(libs))

	for _, lib := range libs {
		// Skip playlist libraries — they change too frequently and aren't useful to diff.
		if lib.CollectionType == "playlists" {
			log.Debug("skipping playlist library", "library", lib.Name)
			continue
		}
		events, err := w.syncLibrary(ctx, lib)
		if err != nil {
			log.Error("sync library failed", "library", lib.Name, "err", err)
			continue
		}
		allEvents = append(allEvents, events...)
	}

	syncSuccess = true

	if w.notifier != nil && len(allEvents) > 0 {
		if err := w.notifier.Notify(ctx, allEvents); err != nil {
			log.Error("notification failed", "err", err)
		}
	}

	log.Info("sync complete", "duration", time.Since(start), "events", len(allEvents))
}

// seasonKey uniquely identifies a season within a series.
type seasonKey struct {
	SeriesID string
	SeasonID string
}

// seasonGroup accumulates episodes from one season during a sync.
type seasonGroup struct {
	SeriesName     string
	SeasonName     string
	SeasonNumber   int
	SeriesImageTag string // image tag from the first episode that has one
	Episodes       []jellyfin.Item
}

type candidateItem struct {
	item     jellyfin.Item
	si       storage.Item
	targetID string
	fp       string
}

func (w *Worker) syncLibrary(ctx context.Context, lib jellyfin.Library) ([]storage.Event, error) {
	log := slog.With("library", lib.Name, "library_id", lib.ID)

	// Single query: load id + title + image_tag for all known items.
	prevItems, err := w.store.GetCurrentItems(ctx, lib.ID)
	if err != nil {
		return nil, err
	}

	items, err := w.client.GetItems(ctx, lib.ID)
	if err != nil {
		return nil, err
	}
	log.Debug("fetched items from jellyfin", "count", len(items))

	now := time.Now().UTC()
	newIDs := make(map[string]bool, len(items))
	storageItems := make([]storage.Item, 0, len(items))
	var metadataUpdates []storage.MetadataUpdate

	var unseenCandidates []candidateItem
	addedByFP := make(map[string]candidateItem)

	for _, item := range items {
		newIDs[item.ID] = true
		si, targetID, fp := processJellyfinItem(item, lib.ID, now)
		storageItems = append(storageItems, si)

		if prev, known := prevItems[item.ID]; known {
			if prev.Title != si.Title || prev.ImageTag != si.ImageTag {
				metadataUpdates = append(metadataUpdates, storage.MetadataUpdate{
					EventID:  targetID,
					Title:    si.Title,
					ImageTag: si.ImageTag,
				})
			}
		} else {
			candidate := candidateItem{item: item, si: si, targetID: targetID, fp: fp}
			unseenCandidates = append(unseenCandidates, candidate)
			addedByFP[fp] = candidate
		}
	}

	// Upgrade detection and removals
	var eventUpgrades []storage.EventUpgrade
	var upgradedOldIDs []string
	upgradedFPs := make(map[string]bool)
	var removedIDs []string
	var removedEvents []storage.Event

	for id, prev := range prevItems {
		if newIDs[id] {
			continue // still present
		}
		fp := storedFingerprintFromMeta(prev)
		if candidate, matched := addedByFP[fp]; matched {
			// Upgrade: update existing events to point to the new Jellyfin ID and image tag,
			// mark old ID removed silently, suppress both events.
			log.Debug("upgrade detected, updating events and suppressing new event",
				"title", prev.Title, "old_id", id, "new_id", candidate.item.ID)
			upgradedOldIDs = append(upgradedOldIDs, id)
			upgradedFPs[fp] = true

			oldTargetID := targetIDFromMeta(id, prev)
			eventUpgrades = append(eventUpgrades, storage.EventUpgrade{
				OldID:       id,
				NewTargetID: candidate.targetID,
				NewImageTag: candidate.si.ImageTag,
			})
			if oldTargetID != id {
				eventUpgrades = append(eventUpgrades, storage.EventUpgrade{
					OldID:       oldTargetID,
					NewTargetID: candidate.targetID,
					NewImageTag: candidate.si.ImageTag,
				})
			}
		} else {
			removedIDs = append(removedIDs, id)
			if prev.MediaType != "Series" {
				targetID := targetIDFromMeta(id, prev)
				removedEvents = append(removedEvents, storage.Event{
					JellyfinID:  targetID,
					EventType:   "removed",
					OccurredAt:  now,
					LibraryName: lib.Name,
					Title:       prev.Title,
					MediaType:   prev.MediaType,
					ImageTag:    prev.ImageTag,
				})
			}
		}
	}

	// Additions
	var addedEvents []storage.Event
	var newEpisodes []jellyfin.Item

	for _, candidate := range unseenCandidates {
		if upgradedFPs[candidate.fp] {
			continue // upgrade, skip event
		}
		item := candidate.item
		if item.Type == "Episode" && item.SeriesID != "" && item.SeasonID != "" {
			newEpisodes = append(newEpisodes, item)
		} else if item.Type != "Series" {
			addedEvents = append(addedEvents, storage.Event{
				JellyfinID:  candidate.targetID,
				EventType:   "added",
				OccurredAt:  now,
				Title:       candidate.si.Title,
				LibraryName: lib.Name,
				MediaType:   item.Type,
				ImageTag:    candidate.si.ImageTag,
			})
		}
	}

	// Episode additions → group by season, emit one event per season group.
	addedSeasons := groupEpisodesBySeason(newEpisodes)
	for _, g := range addedSeasons {
		addedEvents = append(addedEvents, storage.Event{
			JellyfinID:  g.Episodes[0].SeriesID,
			EventType:   "added",
			OccurredAt:  now,
			Title:       seasonTitle(g),
			LibraryName: lib.Name,
			MediaType:   "Episode",
			ImageTag:    g.SeriesImageTag,
		})
	}

	syncData := storage.LibrarySyncData{
		Snapshot: storage.Snapshot{
			TakenAt:     now,
			LibraryID:   lib.ID,
			LibraryName: lib.Name,
			ItemCount:   len(items),
		},
		Items:           storageItems,
		MetadataUpdates: metadataUpdates,
		EventUpgrades:   eventUpgrades,
		UpgradedOldIDs:  upgradedOldIDs,
		AddedEvents:     addedEvents,
		RemovedEvents:   removedEvents,
		RemovedIDs:      removedIDs,
	}

	events, err := w.store.SaveLibrarySync(ctx, syncData)
	if err != nil {
		return nil, fmt.Errorf("save library sync: %w", err)
	}

	log.Info("library synced",
		"added", countByType(events, "added"),
		"removed", countByType(events, "removed"),
	)
	return events, nil
}

func processJellyfinItem(item jellyfin.Item, libID string, now time.Time) (storage.Item, string, string) {
	targetID := item.ID
	imageTag := item.ImageTags.Primary
	sortTitle := item.SortName
	title := item.Name
	addedAt, _ := time.Parse(time.RFC3339, item.DateCreated)

	if item.Type == "Episode" {
		if item.SeriesPrimaryImageTag != "" {
			imageTag = item.SeriesPrimaryImageTag
		}
		if item.SeriesID != "" {
			targetID = item.SeriesID
		}
		if item.SeriesName != "" {
			sortTitle = fmt.Sprintf("ep|%s|%d|%d|%d|%s",
				strings.ToLower(strings.TrimSpace(item.SeriesName)),
				item.ParentIndexNumber, item.IndexNumber, item.ProductionYear,
				item.SeriesID)
		}
		title = formatEpisodeTitle(item.SeriesName, item.SeasonName, item.Name, item.ParentIndexNumber, item.IndexNumber)
	} else if item.Type == "Audio" {
		if item.AlbumPrimaryImageTag != "" {
			imageTag = item.AlbumPrimaryImageTag
		}
		if item.AlbumID != "" {
			targetID = item.AlbumID
		}
		if item.AlbumID != "" || item.Album != "" {
			sortTitle = fmt.Sprintf("audio|%s|%s", item.AlbumID, item.Album)
		}
		title = formatAudioTitle(item.Album, item.Name)
	}

	si := storage.Item{
		JellyfinID: item.ID,
		LibraryID:  libID,
		Title:      title,
		SortTitle:  sortTitle,
		MediaType:  item.Type,
		Year:       item.ProductionYear,
		ImageTag:   imageTag,
		AddedAt:    addedAt,
		LastSeenAt: now,
	}

	fp := itemFingerprint(item)
	return si, targetID, fp
}

func targetIDFromMeta(id string, meta storage.ItemMeta) string {
	if meta.MediaType == "Episode" && strings.HasPrefix(meta.SortTitle, "ep|") {
		parts := strings.Split(meta.SortTitle, "|")
		if len(parts) >= 6 && parts[5] != "" {
			return parts[5]
		}
	} else if meta.MediaType == "Audio" && strings.HasPrefix(meta.SortTitle, "audio|") {
		parts := strings.Split(meta.SortTitle, "|")
		if len(parts) >= 2 && parts[1] != "" {
			return parts[1]
		}
	}
	return id
}


// formatEpisodeTitle constructs a title showing series, season/episode numbers, and episode title.
// e.g. "Severance — S01E01 — Good News About Hell"
func formatEpisodeTitle(seriesName, seasonName, epTitle string, seasonNum, epNum int) string {
	seriesName = strings.TrimSpace(seriesName)
	epTitle = strings.TrimSpace(epTitle)
	seasonName = strings.TrimSpace(seasonName)

	if seriesName == "" {
		if seasonNum > 0 && epNum > 0 && epTitle != "" {
			return fmt.Sprintf("S%02dE%02d — %s", seasonNum, epNum, epTitle)
		}
		return epTitle
	}

	// Avoid duplicating series name if epTitle already starts with it
	if strings.HasPrefix(strings.ToLower(epTitle), strings.ToLower(seriesName)) {
		return epTitle
	}

	if seasonNum > 0 && epNum > 0 {
		if epTitle != "" {
			return fmt.Sprintf("%s — S%02dE%02d — %s", seriesName, seasonNum, epNum, epTitle)
		}
		return fmt.Sprintf("%s — S%02dE%02d", seriesName, seasonNum, epNum)
	}
	if seasonNum > 0 {
		if epTitle != "" {
			return fmt.Sprintf("%s — Season %d — %s", seriesName, seasonNum, epTitle)
		}
		return fmt.Sprintf("%s — Season %d", seriesName, seasonNum)
	}
	if epNum > 0 {
		if epTitle != "" {
			return fmt.Sprintf("%s — E%02d — %s", seriesName, epNum, epTitle)
		}
		return fmt.Sprintf("%s — E%02d", seriesName, epNum)
	}
	if seasonName != "" && !strings.EqualFold(seasonName, "Season 0") && !strings.EqualFold(seasonName, "Specials") {
		if epTitle != "" {
			return fmt.Sprintf("%s — %s — %s", seriesName, seasonName, epTitle)
		}
		return fmt.Sprintf("%s — %s", seriesName, seasonName)
	}
	if epTitle != "" {
		return fmt.Sprintf("%s — %s", seriesName, epTitle)
	}
	return seriesName
}

// formatAudioTitle constructs a title showing album name and track title.
// e.g. "Abbey Road — Come Together"
func formatAudioTitle(albumName, trackTitle string) string {
	albumName = strings.TrimSpace(albumName)
	trackTitle = strings.TrimSpace(trackTitle)

	if albumName == "" {
		return trackTitle
	}
	if strings.HasPrefix(strings.ToLower(trackTitle), strings.ToLower(albumName)) {
		return trackTitle
	}
	if trackTitle != "" {
		return fmt.Sprintf("%s — %s", albumName, trackTitle)
	}
	return albumName
}

// itemFingerprint returns a stable key for a Jellyfin item used to detect upgrades.
// Episodes are keyed by series+season+episode number+year; everything else by title+type+year.
func itemFingerprint(item jellyfin.Item) string {
	if item.Type == "Episode" && item.SeriesName != "" {
		return fmt.Sprintf("ep|%s|%d|%d|%d", strings.ToLower(strings.TrimSpace(item.SeriesName)), item.ParentIndexNumber, item.IndexNumber, item.ProductionYear)
	}
	title := item.Name
	if item.Type == "Audio" {
		title = formatAudioTitle(item.Album, item.Name)
	}
	return fmt.Sprintf("%s|%s|%d", strings.ToLower(strings.TrimSpace(title)), strings.ToLower(item.Type), item.ProductionYear)
}

// storedFingerprintFromMeta returns the fingerprint from an ItemMeta (loaded from the DB).
func storedFingerprintFromMeta(meta storage.ItemMeta) string {
	if meta.MediaType == "Episode" && strings.HasPrefix(meta.SortTitle, "ep|") {
		parts := strings.Split(meta.SortTitle, "|")
		if len(parts) >= 5 {
			return strings.Join(parts[:5], "|")
		}
		return meta.SortTitle
	}
	return fmt.Sprintf("%s|%s|%d", strings.ToLower(strings.TrimSpace(meta.Title)), strings.ToLower(meta.MediaType), meta.Year)
}

// groupEpisodesBySeason groups a slice of episodes by their season.
func groupEpisodesBySeason(episodes []jellyfin.Item) []seasonGroup {
	index := make(map[seasonKey]int) // key → index in groups
	var groups []seasonGroup

	for _, ep := range episodes {
		k := seasonKey{SeriesID: ep.SeriesID, SeasonID: ep.SeasonID}
		imageTag := ep.SeriesPrimaryImageTag
		if imageTag == "" {
			imageTag = ep.ImageTags.Primary
		}

		if i, ok := index[k]; ok {
			groups[i].Episodes = append(groups[i].Episodes, ep)
			if groups[i].SeriesImageTag == "" && imageTag != "" {
				groups[i].SeriesImageTag = imageTag
			}
		} else {
			index[k] = len(groups)
			groups = append(groups, seasonGroup{
				SeriesName:     ep.SeriesName,
				SeasonName:     ep.SeasonName,
				SeasonNumber:   ep.ParentIndexNumber,
				SeriesImageTag: imageTag,
				Episodes:       []jellyfin.Item{ep},
			})
		}
	}
	return groups
}

// seasonTitle builds a human-readable event title for a season group.
func seasonTitle(g seasonGroup) string {
	n := len(g.Episodes)
	if n == 1 {
		ep := g.Episodes[0]
		return formatEpisodeTitle(g.SeriesName, g.SeasonName, ep.Name, ep.ParentIndexNumber, ep.IndexNumber)
	}
	eps := "episodes"
	if g.SeasonName != "" {
		return fmt.Sprintf("%s — %s (%d %s)", g.SeriesName, g.SeasonName, n, eps)
	}
	if g.SeasonNumber > 0 {
		return fmt.Sprintf("%s — Season %d (%d %s)", g.SeriesName, g.SeasonNumber, n, eps)
	}
	return fmt.Sprintf("%s (%d %s)", g.SeriesName, n, eps)
}

func countByType(events []storage.Event, t string) int {
	n := 0
	for _, e := range events {
		if e.EventType == t {
			n++
		}
	}
	return n
}
