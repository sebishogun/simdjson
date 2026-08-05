package simdjson

// The dispatch floor, done properly.
//
// An earlier version of this measured a hand-written encoder using
// strconv.AppendInt and a byte-at-a-time escape scan, and came out 17% SLOWER
// than the compiled encoder. That was taken as evidence that per-field dispatch
// is not the cost -- but it measured the primitives, not the dispatch, because
// this package's appendInt and appendQuoted beat the ones it used.
//
// This one uses the package's own primitives. It differs from the compiled
// encoder in one thing only: the field sequence is straight-line code instead of
// a loop over a table with a function call per field. So the difference between
// them is the dispatch, and nothing else.
//
//	hand-written, our primitives   41,394 ns
//	compiled encoder               50,530
//
// 18%. It is not the encodeFn calls -- the leaf set covers string, int64/int,
// uint64/uint, bool and float64, so only the nested User and the Statuses slice
// reach f.fn here, 101 calls against roughly 2,100 field writes. It is the loop:
// per field, load &fields[i], test f.simple, test omitAll, compute
// unsafe.Add(p, f.offset), switch on f.leaf. Generated code has those as
// constants and does none of it.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"testing"
)

type fUser struct {
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

type fStatus struct {
	CreatedAt string `json:"created_at"`
	ID        int64  `json:"id"`
	IDStr     string `json:"id_str"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
	User      fUser  `json:"user"`
	RetweetCt int    `json:"retweet_count"`
	FavoriteC int    `json:"favorite_count"`
	Favorited bool   `json:"favorited"`
	Retweeted bool   `json:"retweeted"`
	Lang      string `json:"lang"`
}

type fSearch struct {
	Statuses []fStatus `json:"statuses"`
}

func fUserTo(b []byte, u *fUser) []byte {
	b = append(b, `{"id":`...)
	b = appendInt(b, u.ID)
	b = append(b, `,"id_str":`...)
	b = appendQuoted(b, u.IDStr)
	b = append(b, `,"name":`...)
	b = appendQuoted(b, u.Name)
	b = append(b, `,"screen_name":`...)
	b = appendQuoted(b, u.ScreenName)
	b = append(b, `,"location":`...)
	b = appendQuoted(b, u.Location)
	b = append(b, `,"description":`...)
	b = appendQuoted(b, u.Description)
	b = append(b, `,"followers_count":`...)
	b = appendInt(b, int64(u.FollowersCount))
	b = append(b, `,"friends_count":`...)
	b = appendInt(b, int64(u.FriendsCount))
	b = append(b, `,"verified":`...)
	if u.Verified {
		b = append(b, "true"...)
	} else {
		b = append(b, "false"...)
	}
	b = append(b, `,"statuses_count":`...)
	b = appendInt(b, int64(u.StatusesCount))
	b = append(b, `,"lang":`...)
	b = appendQuoted(b, u.Lang)
	b = append(b, `,"profile_image_url":`...)
	b = appendQuoted(b, u.ProfileImageURL)
	return append(b, '}')
}

func fStatusTo(b []byte, s *fStatus) []byte {
	b = append(b, `{"created_at":`...)
	b = appendQuoted(b, s.CreatedAt)
	b = append(b, `,"id":`...)
	b = appendInt(b, s.ID)
	b = append(b, `,"id_str":`...)
	b = appendQuoted(b, s.IDStr)
	b = append(b, `,"text":`...)
	b = appendQuoted(b, s.Text)
	b = append(b, `,"source":`...)
	b = appendQuoted(b, s.Source)
	b = append(b, `,"truncated":`...)
	if s.Truncated {
		b = append(b, "true"...)
	} else {
		b = append(b, "false"...)
	}
	b = append(b, `,"user":`...)
	b = fUserTo(b, &s.User)
	b = append(b, `,"retweet_count":`...)
	b = appendInt(b, int64(s.RetweetCt))
	b = append(b, `,"favorite_count":`...)
	b = appendInt(b, int64(s.FavoriteC))
	b = append(b, `,"favorited":`...)
	if s.Favorited {
		b = append(b, "true"...)
	} else {
		b = append(b, "false"...)
	}
	b = append(b, `,"retweeted":`...)
	if s.Retweeted {
		b = append(b, "true"...)
	} else {
		b = append(b, "false"...)
	}
	b = append(b, `,"lang":`...)
	b = appendQuoted(b, s.Lang)
	return append(b, '}')
}

func fSearchTo(b []byte, v *fSearch) []byte {
	b = append(b, `{"statuses":[`...)
	for i := range v.Statuses {
		if i > 0 {
			b = append(b, ',')
		}
		b = fStatusTo(b, &v.Statuses[i])
	}
	return append(b, `]}`...)
}

func loadFSearch(tb testing.TB) fSearch {
	tb.Helper()
	f, err := os.Open("testdata/bench/corpus/twitter.json.gz")
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		tb.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		tb.Fatal(err)
	}
	var v fSearch
	if err := json.Unmarshal(data, &v); err != nil {
		tb.Fatal(err)
	}
	return v
}

func TestDispatchFloorMatchesStdlib(t *testing.T) {
	v := loadFSearch(t)
	want, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got := fSearchTo(make([]byte, 0, len(want)), &v)
	if !bytes.Equal(got, want) {
		t.Fatalf("hand-written differs from encoding/json\n got %.200s\nwant %.200s", got, want)
	}
}

func BenchmarkDispatchFloor(b *testing.B) {
	v := loadFSearch(b)
	want, _ := json.Marshal(v)
	buf := make([]byte, 0, len(want)+1024)
	b.SetBytes(int64(len(want)))
	b.Run("handwritten-our-primitives", func(b *testing.B) {
		for b.Loop() {
			buf = fSearchTo(buf[:0], &v)
		}
	})
	b.Run("compiled", func(b *testing.B) {
		for b.Loop() {
			var err error
			buf, err = MarshalTo(buf[:0], v)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
