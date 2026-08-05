package bench

// The types the generated-encoder row marshals.
//
// They mirror tUser/tStatus/tSearch field for field and tag for tag, but they
// are DISTINCT defined types, not aliases: an alias shares type identity, so
// registering an encoder for it would silently register one for tSearch and
// turn the reflect rows into generated rows. tSearch itself also cannot be
// generated for -- its metadata is an anonymous struct, which structgen
// declines -- so the metadata is a named type here. Identical tags mean
// identical JSON, which TestGeneratedRowMatches asserts before the row is
// worth timing.
//
//go:generate sh -c "cd ../tools && go run ./structgen -dir $(pwd)/../bench -types GSearch"

type GUser struct {
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

type GStatus struct {
	CreatedAt string `json:"created_at"`
	ID        int64  `json:"id"`
	IDStr     string `json:"id_str"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
	User      GUser  `json:"user"`
	RetweetCt int    `json:"retweet_count"`
	FavoriteC int    `json:"favorite_count"`
	Favorited bool   `json:"favorited"`
	Retweeted bool   `json:"retweeted"`
	Lang      string `json:"lang"`
}

type GMeta struct {
	CompletedIn float64 `json:"completed_in"`
	MaxID       int64   `json:"max_id"`
	Query       string  `json:"query"`
	Count       int     `json:"count"`
	SinceID     int64   `json:"since_id"`
}

type GSearch struct {
	Statuses       []GStatus `json:"statuses"`
	SearchMetadata GMeta     `json:"search_metadata"`
}
