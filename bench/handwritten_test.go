package bench

// The floor.
//
// Everything measured so far says our struct encode is behind sonic by an
// amount no single item in the profile accounts for. The one structural
// difference left is that sonic compiles the field sequence to machine code at
// run time and we make an encodeFn call per field. That is consistent with a
// diffuse deficit and with no hotspot showing it -- and it is also the kind of
// claim that has been wrong four times today, so it gets measured rather than
// asserted.
//
// This is what a compiler would emit if it were perfect: the same fields in the
// same order, no reflection, no dispatch, no options to consult. If it lands
// near sonic then per-field dispatch is the gap and build-time codegen is the
// answer. If it lands near ours then dispatch is not the gap and the search
// goes elsewhere.
//
// It writes the same bytes as encoding/json for this input, which the test
// below checks -- otherwise it is measuring a different job, which is the error
// that produced three wrong conclusions today.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	ours "github.com/sebishogun/simdjson"
)

// appendStr writes a JSON string, escaping only what this corpus contains.
// Not general -- the test asserts byte-identity with encoding/json on the real
// input, which is what makes that acceptable here.
func appendStr(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
			continue
		}
		b = append(b, s[start:i]...)
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			const hex = "0123456789abcdef"
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
		}
		start = i + 1
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

func appendUser(b []byte, u *tUser) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, u.ID, 10)
	b = append(b, `,"id_str":`...)
	b = appendStr(b, u.IDStr)
	b = append(b, `,"name":`...)
	b = appendStr(b, u.Name)
	b = append(b, `,"screen_name":`...)
	b = appendStr(b, u.ScreenName)
	b = append(b, `,"location":`...)
	b = appendStr(b, u.Location)
	b = append(b, `,"description":`...)
	b = appendStr(b, u.Description)
	b = append(b, `,"followers_count":`...)
	b = strconv.AppendInt(b, int64(u.FollowersCount), 10)
	b = append(b, `,"friends_count":`...)
	b = strconv.AppendInt(b, int64(u.FriendsCount), 10)
	b = append(b, `,"verified":`...)
	b = strconv.AppendBool(b, u.Verified)
	b = append(b, `,"statuses_count":`...)
	b = strconv.AppendInt(b, int64(u.StatusesCount), 10)
	b = append(b, `,"lang":`...)
	b = appendStr(b, u.Lang)
	b = append(b, `,"profile_image_url":`...)
	b = appendStr(b, u.ProfileImageURL)
	return append(b, '}')
}

func appendStatus(b []byte, s *tStatus) []byte {
	b = append(b, `{"created_at":`...)
	b = appendStr(b, s.CreatedAt)
	b = append(b, `,"id":`...)
	b = strconv.AppendInt(b, s.ID, 10)
	b = append(b, `,"id_str":`...)
	b = appendStr(b, s.IDStr)
	b = append(b, `,"text":`...)
	b = appendStr(b, s.Text)
	b = append(b, `,"source":`...)
	b = appendStr(b, s.Source)
	b = append(b, `,"truncated":`...)
	b = strconv.AppendBool(b, s.Truncated)
	b = append(b, `,"user":`...)
	b = appendUser(b, &s.User)
	b = append(b, `,"retweet_count":`...)
	b = strconv.AppendInt(b, int64(s.RetweetCt), 10)
	b = append(b, `,"favorite_count":`...)
	b = strconv.AppendInt(b, int64(s.FavoriteC), 10)
	b = append(b, `,"favorited":`...)
	b = strconv.AppendBool(b, s.Favorited)
	b = append(b, `,"retweeted":`...)
	b = strconv.AppendBool(b, s.Retweeted)
	b = append(b, `,"lang":`...)
	b = appendStr(b, s.Lang)
	return append(b, '}')
}

func appendSearch(b []byte, v *tSearch) []byte {
	b = append(b, `{"statuses":[`...)
	for i := range v.Statuses {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendStatus(b, &v.Statuses[i])
	}
	b = append(b, `],"search_metadata":{"completed_in":`...)
	b = strconv.AppendFloat(b, v.SearchMetadata.CompletedIn, 'g', -1, 64)
	b = append(b, `,"max_id":`...)
	b = strconv.AppendInt(b, v.SearchMetadata.MaxID, 10)
	b = append(b, `,"query":`...)
	b = appendStr(b, v.SearchMetadata.Query)
	b = append(b, `,"count":`...)
	b = strconv.AppendInt(b, int64(v.SearchMetadata.Count), 10)
	b = append(b, `,"since_id":`...)
	b = strconv.AppendInt(b, v.SearchMetadata.SinceID, 10)
	return append(b, `}}`...)
}

func loadSearch(tb testing.TB) tSearch {
	tb.Helper()
	var v tSearch
	if err := json.Unmarshal(loadCorpus(tb, "twitter"), &v); err != nil {
		tb.Fatal(err)
	}
	return v
}

// The hand-written encoder must produce exactly what encoding/json does, or it
// is measuring a different job.
func TestHandwrittenMatchesStdlib(t *testing.T) {
	v := loadSearch(t)
	want, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got := appendSearch(make([]byte, 0, len(want)), &v)
	if !bytes.Equal(got, want) {
		for i := range got {
			if i >= len(want) || got[i] != want[i] {
				lo := i - 60
				if lo < 0 {
					lo = 0
				}
				hi := i + 60
				if hi > len(got) {
					hi = len(got)
				}
				t.Fatalf("differs at %d:\n hand %q\n  std %q", i, got[lo:hi], want[lo:min(hi, len(want))])
			}
		}
		t.Fatalf("lengths differ: hand %d, std %d", len(got), len(want))
	}
}

func BenchmarkHandwritten(b *testing.B) {
	v := loadSearch(b)
	want, _ := json.Marshal(v)
	buf := make([]byte, 0, len(want))
	b.SetBytes(int64(len(want)))
	b.Run("floor", func(b *testing.B) {
		for b.Loop() {
			buf = appendSearch(buf[:0], &v)
		}
	})
	b.Run("ours-MarshalTo", func(b *testing.B) {
		for b.Loop() {
			var err error
			buf, err = ours.MarshalTo(buf[:0], v)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
