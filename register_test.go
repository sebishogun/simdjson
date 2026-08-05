package simdjson

// Does a registered encoder actually reach the floor through the public API?
//
// The floor benchmark measures straight-line code called directly. That says
// what is available; it does not say that the registration seam delivers it,
// because Marshal has its own path in front of it -- the pooled state, the
// top-level dispatch, the interface unwrap. If the seam eats the difference
// there is no point generating anything.
//
// The encoders below are what a generator would emit for the floor test's
// types: the same field order, offsets and key bytes as constants, and this
// package's own primitives for the values.

import (
	"bytes"
	stdjson "encoding/json"
	"testing"
	"unsafe"
)

func genUser(dst []byte, p unsafe.Pointer, o Options) []byte {
	u := (*fUser)(p)
	dst = append(dst, `{"id":`...)
	dst = AppendInt(dst, u.ID)
	dst = append(dst, `,"id_str":`...)
	dst = AppendString(dst, u.IDStr, o)
	dst = append(dst, `,"name":`...)
	dst = AppendString(dst, u.Name, o)
	dst = append(dst, `,"screen_name":`...)
	dst = AppendString(dst, u.ScreenName, o)
	dst = append(dst, `,"location":`...)
	dst = AppendString(dst, u.Location, o)
	dst = append(dst, `,"description":`...)
	dst = AppendString(dst, u.Description, o)
	dst = append(dst, `,"followers_count":`...)
	dst = AppendInt(dst, int64(u.FollowersCount))
	dst = append(dst, `,"friends_count":`...)
	dst = AppendInt(dst, int64(u.FriendsCount))
	dst = append(dst, `,"verified":`...)
	dst = AppendBool(dst, u.Verified)
	dst = append(dst, `,"statuses_count":`...)
	dst = AppendInt(dst, int64(u.StatusesCount))
	dst = append(dst, `,"lang":`...)
	dst = AppendString(dst, u.Lang, o)
	dst = append(dst, `,"profile_image_url":`...)
	dst = AppendString(dst, u.ProfileImageURL, o)
	return append(dst, '}')
}

func genStatus(dst []byte, p unsafe.Pointer, o Options) []byte {
	s := (*fStatus)(p)
	dst = append(dst, `{"created_at":`...)
	dst = AppendString(dst, s.CreatedAt, o)
	dst = append(dst, `,"id":`...)
	dst = AppendInt(dst, s.ID)
	dst = append(dst, `,"id_str":`...)
	dst = AppendString(dst, s.IDStr, o)
	dst = append(dst, `,"text":`...)
	dst = AppendString(dst, s.Text, o)
	dst = append(dst, `,"source":`...)
	dst = AppendString(dst, s.Source, o)
	dst = append(dst, `,"truncated":`...)
	dst = AppendBool(dst, s.Truncated)
	dst = append(dst, `,"user":`...)
	dst = genUser(dst, unsafe.Pointer(&s.User), o)
	dst = append(dst, `,"retweet_count":`...)
	dst = AppendInt(dst, int64(s.RetweetCt))
	dst = append(dst, `,"favorite_count":`...)
	dst = AppendInt(dst, int64(s.FavoriteC))
	dst = append(dst, `,"favorited":`...)
	dst = AppendBool(dst, s.Favorited)
	dst = append(dst, `,"retweeted":`...)
	dst = AppendBool(dst, s.Retweeted)
	dst = append(dst, `,"lang":`...)
	dst = AppendString(dst, s.Lang, o)
	return append(dst, '}')
}

func genSearch(dst []byte, p unsafe.Pointer, o Options) []byte {
	v := (*fSearch)(p)
	dst = append(dst, `{"statuses":[`...)
	for i := range v.Statuses {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = genStatus(dst, unsafe.Pointer(&v.Statuses[i]), o)
	}
	return append(dst, `]}`...)
}

// registered is a separate type from fSearch, so registering an encoder for it
// cannot disturb the floor benchmark's measurement of the compiled path.
type registered fSearch

func genRegistered(dst []byte, p unsafe.Pointer, o Options) []byte {
	return genSearch(dst, p, o)
}

func init() { RegisterEncoder[registered](genRegistered) }

// The whole point: identical bytes. A registered encoder that is faster and
// wrong is worth nothing, and nothing at run time checks it.
func TestRegisteredEncoderMatchesMarshal(t *testing.T) {
	v := loadFSearch(t)
	r := registered(v)
	got, err := Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		for i := range got {
			if i >= len(want) || got[i] != want[i] {
				lo := max(0, i-60)
				t.Fatalf("differs at %d:\n  gen %q\n ours %q", i,
					got[lo:min(i+60, len(got))], want[lo:min(i+60, len(want))])
			}
		}
		t.Fatalf("lengths differ: gen %d, ours %d", len(got), len(want))
	}
	std, err := stdjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, std) {
		t.Fatal("registered encoder disagrees with encoding/json")
	}
}

func BenchmarkRegistered(b *testing.B) {
	v := loadFSearch(b)
	r := registered(v)
	buf := make([]byte, 0, 1<<20)
	b.Run("compiled", func(b *testing.B) {
		for b.Loop() {
			var err error
			buf, err = MarshalTo(buf[:0], v)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("registered", func(b *testing.B) {
		for b.Loop() {
			var err error
			buf, err = MarshalTo(buf[:0], r)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
