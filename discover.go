package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ── MusicBrainz types ─────────────────────────────────────────────────────────

type mbArtistCredit struct {
	Name   string `json:"name"`
	Artist struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
}

type mbMedia struct {
	Format string `json:"format"`
}

type mbRelease struct {
	ID             string           `json:"id"`
	Title          string           `json:"title"`
	Date           string           `json:"date"`
	Country        string           `json:"country"`
	Disambiguation string           `json:"disambiguation"`
	TextRepresentation struct {
		Language string `json:"language"`
	} `json:"text-representation"`
	Media        []mbMedia        `json:"media"`
	ArtistCredit []mbArtistCredit `json:"artist-credit"`
	ReleaseGroup struct {
		PrimaryType string `json:"primary-type"`
	} `json:"release-group"`
}

type mbArtist struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Country        string `json:"country"`
	Disambiguation string `json:"disambiguation"`
}

type mbReleaseGroup struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	PrimaryType      string `json:"primary-type"`
	FirstReleaseDate string `json:"first-release-date"`
}

func mbGet(path string, out interface{}) error {
	req, err := http.NewRequest("GET", "https://musicbrainz.org"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "music-importer/1.0 (https://github.com/gabehf/music-importer)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MusicBrainz returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func searchMBReleases(query string) ([]mbRelease, error) {
	var result struct {
		Releases []mbRelease `json:"releases"`
	}
	err := mbGet("/ws/2/release/?query="+url.QueryEscape(query)+"&fmt=json&limit=20&inc=media", &result)
	return result.Releases, err
}

func searchMBArtists(query string) ([]mbArtist, error) {
	var result struct {
		Artists []mbArtist `json:"artists"`
	}
	err := mbGet("/ws/2/artist/?query="+url.QueryEscape(query)+"&fmt=json&limit=20", &result)
	return result.Artists, err
}

// releaseFormatScore returns a preference score for a release's media format.
// Higher is better. CD=2, Digital Media=1, anything else=0.
func releaseFormatScore(r mbRelease) int {
	for _, m := range r.Media {
		switch m.Format {
		case "Digital Media":
			return 2
		case "CD":
			return 1
		}
	}
	return 0
}

// releaseCountryScore returns a preference score for a release's country.
// Higher is better. KR=3, JP=2, XW=1, anything else=0.
func releaseCountryScore(r mbRelease) int {
	switch r.Country {
	case "XW":
		return 2
	case "KR":
		return 1
	}
	return 0
}

// returns true if strings formatted 'YYYY-MM-DD" ts1 is before ts2
func timeStringIsBefore(ts1, ts2 string) (bool, error) {
	datefmt := "2006-02-01"
	t1, err := time.Parse(datefmt, ts1)
	if err != nil {
		return false, err
	}
	t2, err := time.Parse(datefmt, ts2)
	if err != nil {
		return false, err
	}
	return t1.Unix() <= t2.Unix(), nil
}

// pickBestRelease selects the preferred release from a list.
// Format (CD > Digital Media > *) is the primary sort key;
// country (KR > JP > XW > *) breaks ties.
func pickBestRelease(releases []mbRelease) *mbRelease {
	if len(releases) == 0 {
		return nil
	}
	best := &releases[0]
	for i := 1; i < len(releases); i++ {
		r := &releases[i]
		if before, err := timeStringIsBefore(r.Date, best.Date); before && err == nil {
			rf, bf := releaseFormatScore(*r), releaseFormatScore(*best)
			if rf > bf || (rf == bf && releaseCountryScore(*r) > releaseCountryScore(*best)) {
				best = r
			}
		}
	}
	return best
}

// pickBestReleaseForGroup fetches all releases for a release group via the
// MusicBrainz browse API (with media info) and returns the preferred release.
// Returns nil on error or when the group has no releases.
func pickBestReleaseForGroup(rgMBID string) *mbRelease {
	var result struct {
		Releases []mbRelease `json:"releases"`
	}
	path := fmt.Sprintf("/ws/2/release?release-group=%s&fmt=json&inc=media&limit=100", url.QueryEscape(rgMBID))
	if err := mbGet(path, &result); err != nil || len(result.Releases) == 0 {
		return nil
	}
	return pickBestRelease(result.Releases)
}

// getMBArtistReleaseGroups returns all Album and EP release groups for an artist,
// paginating through the MusicBrainz browse API with the required 1 req/s rate limit.
func getMBArtistReleaseGroups(artistMBID string) ([]mbReleaseGroup, error) {
	const limit = 100
	var all []mbReleaseGroup

	for offset := 0; ; offset += limit {
		path := fmt.Sprintf(
			"/ws/2/release-group?artist=%s&type=album%%7Cep&fmt=json&limit=%d&offset=%d",
			url.QueryEscape(artistMBID), limit, offset,
		)

		var result struct {
			ReleaseGroups []mbReleaseGroup `json:"release-groups"`
			Count         int              `json:"release-group-count"`
		}
		if err := mbGet(path, &result); err != nil {
			return all, err
		}

		for _, rg := range result.ReleaseGroups {
			t := strings.ToLower(rg.PrimaryType)
			if t == "album" || t == "ep" {
				all = append(all, rg)
			}
		}

		if offset+limit >= result.Count {
			break
		}
		time.Sleep(time.Second) // MusicBrainz rate limit
	}

	return all, nil
}

// fetchArtist fetches every Album and EP release group for an artist by running
// fetchRelease for each one sequentially, then registers each for monitoring.
func fetchArtist(artistMBID, artistName string, logf func(string)) error {
	log.Printf("[discover] artist fetch started: %s (%s)", artistName, artistMBID)
	logf(fmt.Sprintf("Looking up discography for %s on MusicBrainz…", artistName))

	groups, err := getMBArtistReleaseGroups(artistMBID)
	if err != nil {
		return fmt.Errorf("MusicBrainz discography lookup failed: %w", err)
	}
	if len(groups) == 0 {
		return fmt.Errorf("no albums or EPs found for %s on MusicBrainz", artistName)
	}

	log.Printf("[discover] found %d release groups for %s", len(groups), artistName)
	logf(fmt.Sprintf("Found %d albums/EPs", len(groups)))

	failed := 0
	for i, rg := range groups {
		logf(fmt.Sprintf("[%d/%d] %s", i+1, len(groups), rg.Title))
		// Pick the best release for this group. beets --search-id requires a
		// release MBID; release group MBIDs are not accepted.
		time.Sleep(time.Second) // MusicBrainz rate limit
		rel := pickBestReleaseForGroup(rg.ID)
		releaseMBID := ""
		if rel == nil {
			logf(fmt.Sprintf("  ↳ warning: could not resolve release for group %s, beets will search by name", rg.ID))
		} else {
			releaseMBID = rel.ID
			format := ""
			if len(rel.Media) > 0 {
				format = rel.Media[0].Format
			}
			logf(fmt.Sprintf("  ↳ selected release: %s [%s / %s]", releaseMBID, format, rel.Country))
		}

		folder, err := fetchRelease(artistName, rg.Title, releaseMBID, logf)
		if err != nil {
			log.Printf("[discover] fetch failed for %q by %s: %v", rg.Title, artistName, err)
			logf(fmt.Sprintf("  ↳ failed: %v", err))
			failed++
			continue
		}
		// Key the pending download by release group ID for dedup; beets uses releaseMBID.
		registerDownload(rg.ID, releaseMBID, artistName, rg.Title, folder, nil)
		logf(fmt.Sprintf("  ↳ registered for import (release mbid: %s)", releaseMBID))
	}

	if failed > 0 {
		logf(fmt.Sprintf("Done — %d/%d queued, %d failed", len(groups)-failed, len(groups), failed))
	} else {
		logf(fmt.Sprintf("Done — all %d downloads queued, monitoring for import", len(groups)))
	}
	log.Printf("[discover] artist fetch complete: %s (%d/%d succeeded)", artistName, len(groups)-failed, len(groups))
	return nil
}

// ── Fetch state ───────────────────────────────────────────────────────────────

type fetchEntry struct {
	mu      sync.Mutex
	ID      string   `json:"id"`
	Artist  string   `json:"artist"`
	Album   string   `json:"album"`
	Log     []string `json:"log"`
	Done    bool     `json:"done"`
	Success bool     `json:"success"`
	ErrMsg  string   `json:"error,omitempty"`
}

var (
	fetchesMu sync.Mutex
	fetchMap  = make(map[string]*fetchEntry)
)

func newFetchEntry(id, artist, album string) *fetchEntry {
	e := &fetchEntry{ID: id, Artist: artist, Album: album}
	fetchesMu.Lock()
	fetchMap[id] = e
	fetchesMu.Unlock()
	return e
}

func (e *fetchEntry) appendLog(msg string) {
	e.mu.Lock()
	e.Log = append(e.Log, msg)
	e.mu.Unlock()
}

func (e *fetchEntry) finish(err error) {
	e.mu.Lock()
	e.Done = true
	if err != nil {
		e.ErrMsg = err.Error()
	} else {
		e.Success = true
	}
	e.mu.Unlock()
}

func (e *fetchEntry) snapshot() fetchEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := *e
	cp.Log = append([]string(nil), e.Log...)
	return cp
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// handleDiscoverSearch handles GET /discover/search?q=...&type=release|artist
func handleDiscoverSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	searchType := r.URL.Query().Get("type")
	if searchType == "" {
		searchType = "release"
	}
	log.Printf("[discover] search: type=%s q=%q", searchType, q)

	w.Header().Set("Content-Type", "application/json")

	switch searchType {
	case "artist":
		artists, err := searchMBArtists(q)
		if err != nil {
			log.Printf("[discover] artist search error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[discover] artist search returned %d results", len(artists))
		json.NewEncoder(w).Encode(artists)

	default: // "release"
		releases, err := searchMBReleases(q)
		if err != nil {
			log.Printf("[discover] release search error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[discover] release search returned %d results", len(releases))
		json.NewEncoder(w).Encode(releases)
	}
}

// handleDiscoverFetch handles POST /discover/fetch
// Body: {"id":"mbid","artist":"...","album":"..."}
func handleDiscoverFetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		ID     string `json:"id"`
		Artist string `json:"artist"`
		Album  string `json:"album"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" || body.Artist == "" || body.Album == "" {
		http.Error(w, "id, artist and album are required", http.StatusBadRequest)
		return
	}

	// If a fetch for this ID is already in progress, return its ID without starting a new one.
	fetchesMu.Lock()
	existing := fetchMap[body.ID]
	fetchesMu.Unlock()
	if existing != nil {
		existing.mu.Lock()
		done := existing.Done
		existing.mu.Unlock()
		if !done {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"id": body.ID})
			return
		}
	}

	log.Printf("[discover] starting fetch: %q by %s (mbid: %s)", body.Album, body.Artist, body.ID)
	entry := newFetchEntry(body.ID, body.Artist, body.Album)
	go func() {
		folder, err := fetchRelease(body.Artist, body.Album, body.ID, entry.appendLog)
		if err != nil {
			log.Printf("[discover] fetch failed for %q by %s: %v", body.Album, body.Artist, err)
			entry.finish(err)
			return
		}
		log.Printf("[discover] fetch complete for %q by %s, registering for import", body.Album, body.Artist)
		registerDownload(body.ID, body.ID, body.Artist, body.Album, folder, entry)
		// entry.finish is called by the monitor when import completes
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": body.ID})
}

// handleDiscoverFetchArtist handles POST /discover/fetch/artist
// Body: {"id":"artist-mbid","name":"Artist Name"}
func handleDiscoverFetchArtist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" || body.Name == "" {
		http.Error(w, "id and name are required", http.StatusBadRequest)
		return
	}

	fetchesMu.Lock()
	existing := fetchMap[body.ID]
	fetchesMu.Unlock()
	if existing != nil {
		existing.mu.Lock()
		done := existing.Done
		existing.mu.Unlock()
		if !done {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"id": body.ID})
			return
		}
	}

	log.Printf("[discover] starting artist fetch: %s (%s)", body.Name, body.ID)
	entry := newFetchEntry(body.ID, body.Name, "")
	go func() {
		err := fetchArtist(body.ID, body.Name, entry.appendLog)
		if err != nil {
			log.Printf("[discover] artist fetch failed for %s: %v", body.Name, err)
		} else {
			log.Printf("[discover] artist fetch complete for %s", body.Name)
		}
		entry.finish(err)
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": body.ID})
}

// handleDiscoverFetchStatus handles GET /discover/fetch/status?id=...
func handleDiscoverFetchStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	fetchesMu.Lock()
	entry := fetchMap[id]
	fetchesMu.Unlock()

	if entry == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	snap := entry.snapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

// fetchListItem is a summary of a fetch entry for the list endpoint.
type fetchListItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Done    bool   `json:"done"`
	Success bool   `json:"success"`
}

// handleDiscoverFetchList handles GET /discover/fetch/list
// Returns a summary of all known fetch entries so the frontend can discover
// entries created server-side (e.g. per-album entries from an artist fetch).
func handleDiscoverFetchList(w http.ResponseWriter, r *http.Request) {
	fetchesMu.Lock()
	items := make([]fetchListItem, 0, len(fetchMap))
	for _, e := range fetchMap {
		e.mu.Lock()
		title := e.Artist
		if e.Album != "" {
			title = e.Artist + " \u2014 " + e.Album
		}
		items = append(items, fetchListItem{
			ID:      e.ID,
			Title:   title,
			Done:    e.Done,
			Success: e.Success,
		})
		e.mu.Unlock()
	}
	fetchesMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}
