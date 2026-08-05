package bench

// Mirror types for the easyjson rows. easyjson generates methods on named
// types in a non-test file, so the corpus shapes are restated here with
// identical tags; the benchmarks assert agreement with encoding/json before
// any number counts, which is also what catches these drifting from the
// originals.

//easyjson:json
type ejUser struct {
	ID              int64  `json:"id"`
	IDStr           string `json:"id_str"`
	Name            string `json:"name"`
	ScreenName      string `json:"screen_name"`
	Location        string `json:"location"`
	Description     string `json:"description"`
	FollowersCount  int    `json:"followers_count"`
	FriendsCount    int    `json:"friends_count"`
	Verified        bool   `json:"verified"`
	StatusesCount   int    `json:"statuses_count"`
	Lang            string `json:"lang"`
	ProfileImageURL string `json:"profile_image_url"`
}

//easyjson:json
type ejStatus struct {
	CreatedAt string `json:"created_at"`
	ID        int64  `json:"id"`
	IDStr     string `json:"id_str"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
	User      ejUser `json:"user"`
	RetweetCt int    `json:"retweet_count"`
	FavoriteC int    `json:"favorite_count"`
	Favorited bool   `json:"favorited"`
	Retweeted bool   `json:"retweeted"`
	Lang      string `json:"lang"`
}

//easyjson:json
type ejSearch struct {
	Statuses       []ejStatus `json:"statuses"`
	SearchMetadata struct {
		CompletedIn float64 `json:"completed_in"`
		MaxID       int64   `json:"max_id"`
		Query       string  `json:"query"`
		Count       int     `json:"count"`
		SinceID     int64   `json:"since_id"`
	} `json:"search_metadata"`
}

//easyjson:json
type ejCanada struct {
	Type     string `json:"type"`
	Features []struct {
		Type       string `json:"type"`
		Properties struct {
			Name string `json:"name"`
		} `json:"properties"`
		Geometry struct {
			Type        string         `json:"type"`
			Coordinates [][][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}
