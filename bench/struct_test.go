package bench

import (
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

// Decoding into a struct, which is what most callers actually do — and a
// different problem from decoding into interface{}, because the destination
// types are known so nothing has to be boxed.
type tUser struct {
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

type tStatus struct {
	CreatedAt string `json:"created_at"`
	ID        int64  `json:"id"`
	IDStr     string `json:"id_str"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
	User      tUser  `json:"user"`
	RetweetCt int    `json:"retweet_count"`
	FavoriteC int    `json:"favorite_count"`
	Favorited bool   `json:"favorited"`
	Retweeted bool   `json:"retweeted"`
	Lang      string `json:"lang"`
}

type tSearch struct {
	Statuses       []tStatus `json:"statuses"`
	SearchMetadata struct {
		CompletedIn float64 `json:"completed_in"`
		MaxID       int64   `json:"max_id"`
		Query       string  `json:"query"`
		Count       int     `json:"count"`
		SinceID     int64   `json:"since_id"`
	} `json:"search_metadata"`
}

func BenchmarkUnmarshalStruct(b *testing.B) {
	data := loadCorpus(b, "twitter")

	// Both must actually agree before either number means anything.
	var a, c tSearch
	if err := ours.Unmarshal(data, &a); err != nil {
		b.Fatal(err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		b.Fatal(err)
	}
	if len(a.Statuses) != len(c.Statuses) || a.Statuses[0].Text != c.Statuses[0].Text ||
		a.Statuses[0].User.ScreenName != c.Statuses[0].User.ScreenName ||
		a.SearchMetadata.Query != c.SearchMetadata.Query {
		b.Fatalf("decoders disagree:\n ours=%+v\n std =%+v", a.SearchMetadata, c.SearchMetadata)
	}

	b.Run("ours", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var v tSearch
			if err := ours.Unmarshal(data, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var v tSearch
			if err := json.Unmarshal(data, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("sonic", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var v tSearch
			if err := sonic.Unmarshal(data, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("goccy", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var v tSearch
			if err := gojson.Unmarshal(data, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// Scan+Unmarshal against Parse+Unmarshal: the same decode, with and without
// the grammar descent that proves the parts nobody looks at are well-formed.
// The difference is what a validating Unmarshal costs.
func BenchmarkUnmarshalScanned(b *testing.B) {
	data := loadCorpus(b, "twitter")
	b.Run("ours-Parse+Unmarshal", func(b *testing.B) {
		var p ours.Parser
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			d, err := p.Parse(data)
			if err != nil {
				b.Fatal(err)
			}
			var v tSearch
			if err := d.Unmarshal(&v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ours-Scan+Unmarshal", func(b *testing.B) {
		var p ours.Parser
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			d, err := p.Scan(data)
			if err != nil {
				b.Fatal(err)
			}
			var v tSearch
			if err := d.Unmarshal(&v); err != nil {
				b.Fatal(err)
			}
		}
	})
}
