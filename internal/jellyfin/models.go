package jellyfin

// Library represents a Jellyfin media library (virtual folder / collection).
type Library struct {
	ID             string `json:"Id"`
	Name           string `json:"Name"`
	CollectionType string `json:"CollectionType"` // "movies","tvshows","music","playlists", …
}

// Item represents a single media item returned by the Jellyfin Items API.
type Item struct {
	ID              string   `json:"Id"`
	Name            string   `json:"Name"`
	SortName        string   `json:"SortName"`
	Type            string   `json:"Type"` // Movie, Series, Episode, MusicAlbum, …
	ProductionYear  int      `json:"ProductionYear"`
	Overview        string   `json:"Overview"`
	Genres          []string `json:"Genres"`
	CommunityRating float64  `json:"CommunityRating"`
	DateCreated     string   `json:"DateCreated"`
	ImageTags       struct {
		Primary string `json:"Primary"`
	} `json:"ImageTags"`

	// Episode / season fields (only populated for Type == "Episode")
	SeriesID              string `json:"SeriesId"`
	SeriesName            string `json:"SeriesName"`
	SeriesPrimaryImageTag string `json:"SeriesPrimaryImageTag"`
	SeasonID              string `json:"SeasonId"`
	SeasonName            string `json:"SeasonName"`
	ParentIndexNumber     int    `json:"ParentIndexNumber"` // season number
	IndexNumber           int    `json:"IndexNumber"`       // episode number

	// Audio / Album fields (for songs and albums)
	AlbumID              string `json:"AlbumId"`
	Album                string `json:"Album"`
	AlbumPrimaryImageTag string `json:"AlbumPrimaryImageTag"`
}

// itemsResponse is the envelope returned by /Users/{id}/Items.
type itemsResponse struct {
	Items            []Item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
}

// viewsResponse is the envelope returned by /Users/{id}/Views.
type viewsResponse struct {
	Items []Library `json:"Items"`
}

// AuthResult is returned upon successful authentication by name.
type AuthResult struct {
	UserID      string
	Username    string
	AccessToken string
}

type authRequest struct {
	Username string `json:"Username"`
	Pw       string `json:"Pw"`
}

type authResponse struct {
	User struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
	AccessToken string `json:"AccessToken"`
}

