package sqlkit

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

type pgRow struct {
	ID        int64
	CreatedAt time.Time
}

func pgKey(r pgRow) []any { return []any{r.CreatedAt, r.ID} }

func cursorFor(t *testing.T, opts []PagerOption, values ...any) string {
	t.Helper()

	p := CursorPager(PageRequest{Size: 1}, opts...)
	tok, err := encodeCursorToken(p.fp, values)
	if err != nil {
		t.Fatal(err)
	}

	return tok
}

var descKeys = []PagerOption{Desc("created_at"), Desc("id")}

func TestCursorPager_WhereTuple(t *testing.T) {
	ts := time.Date(2026, 11, 11, 10, 0, 0, 123456000, time.UTC)

	p := CursorPager(PageRequest{Cursor: cursorFor(t, descKeys, ts, int64(42))}, descKeys...)
	if p.Err() != nil {
		t.Fatal(p.Err())
	}

	where, args := p.Where(Dollar(3))
	if where != "(created_at, id) < ($3, $4)" {
		t.Fatalf("where = %q", where)
	}

	if len(args) != 2 || !args[0].(time.Time).Equal(ts) || args[1] != int64(42) {
		t.Fatalf("args = %v", args)
	}

	asc := []PagerOption{Asc("id")}
	p = CursorPager(PageRequest{Cursor: cursorFor(t, asc, int64(7))}, asc...)

	if where, _ = p.Where(Dollar(1)); where != "(id) > ($1)" {
		t.Fatalf("asc where = %q", where)
	}

	// first page and offset: no condition
	if w, a := CursorPager(PageRequest{}, descKeys...).Where(Dollar(1)); w != "" || a != nil {
		t.Fatalf("first page: %q %v", w, a)
	}

	if w, _ := OffsetPager(PageRequest{}, descKeys...).Where(Dollar(1)); w != "" {
		t.Fatalf("offset: %q", w)
	}
}

func TestOffsetPager_LimitOffset(t *testing.T) {
	if got := OffsetPager(PageRequest{}, Asc("id")).LimitOffset(); got != "LIMIT 21" {
		t.Fatalf("got %q", got)
	}

	p := OffsetPager(PageRequest{Page: 3}, Asc("id"))
	if got := p.LimitOffset(); got != "LIMIT 21 OFFSET 40" {
		t.Fatalf("got %q", got)
	}

	if p.Offset() != 40 || p.Limit() != 21 || p.Size() != 20 || p.Page() != 3 {
		t.Fatalf("getters: %d %d %d %d", p.Offset(), p.Limit(), p.Size(), p.Page())
	}
}

func TestPager_OrderBy(t *testing.T) {
	p := OffsetPager(PageRequest{}, Desc("created_at"), Asc("t.id"))
	if got := p.OrderBy(); got != "ORDER BY created_at DESC, t.id ASC" {
		t.Fatalf("got %q", got)
	}

	if cols := p.OrderByColumns(); len(cols) != 2 || cols[0] != "created_at DESC" || cols[1] != "t.id ASC" {
		t.Fatalf("cols = %v", cols)
	}
}

func TestPageOf_TrimsExtraRow(t *testing.T) {
	p := CursorPager(PageRequest{Size: 2}, descKeys...)
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []pgRow{{3, t1}, {2, t1}, {1, t1}}

	res := PageOf(rows, p, pgKey)
	if !res.HasMore || len(res.Items) != 2 || res.NextCursor == nil {
		t.Fatalf("res = %+v", res)
	}

	// the token is built from the last KEPT row (id 2), not from the extra one
	next := CursorPager(PageRequest{Cursor: *res.NextCursor}, descKeys...)
	if _, args := next.Where(Dollar(1)); next.Err() != nil || args[1] != int64(2) {
		t.Fatalf("args = %v, err = %v", args, next.Err())
	}

	res = PageOf(rows[:2], p, pgKey)
	if res.HasMore || res.NextCursor != nil || len(res.Items) != 2 {
		t.Fatalf("exactly size: %+v", res)
	}

	res = PageOf([]pgRow{}, p, pgKey)
	if res.HasMore || res.NextCursor != nil || len(res.Items) != 0 {
		t.Fatalf("empty: %+v", res)
	}
}

func TestPageOf_OffsetAndPanics(t *testing.T) {
	p := OffsetPager(PageRequest{Page: 2, Size: 2}, Asc("id"))

	res := PageOf([]int{1, 2, 3}, p, nil) // key is not called for offset
	if !res.HasMore {
		t.Fatal("HasMore")
	}

	if next := OffsetPager(PageRequest{Cursor: *res.NextCursor, Size: 2}, Asc("id")); next.Offset() != 4 || next.Page() != 3 {
		t.Fatalf("next offset = %d page = %d", next.Offset(), next.Page())
	}

	mustPanic := func(name string, f func()) {
		t.Helper()

		defer func() {
			if recover() == nil {
				t.Errorf("%s: want panic", name)
			}
		}()

		f()
	}

	c := CursorPager(PageRequest{Size: 1}, Asc("id"))
	mustPanic("nil key", func() { PageOf([]int{1, 2}, c, nil) })
	mustPanic("wrong count", func() { PageOf([]int{1, 2}, c, func(int) []any { return nil }) })
	mustPanic("token too long", func() {
		PageOf([]int{1, 2}, c, func(int) []any { return []any{strings.Repeat("x", 300)} })
	})
}

func TestPager_Normalize(t *testing.T) {
	tests := []struct {
		name string
		req  PageRequest
		opts []PagerOption
		size int
		page int
	}{
		{"zero size", PageRequest{}, nil, 20, 1},
		{"too big", PageRequest{Size: 5000}, nil, 1000, 1},
		{"MaxSize raises", PageRequest{Size: 10000}, []PagerOption{MaxSize(5000)}, 5000, 1},
		{"MaxSize lowers default", PageRequest{}, []PagerOption{MaxSize(10)}, 10, 1},
		{"negative page", PageRequest{Page: -3, Size: 5}, nil, 5, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := OffsetPager(tt.req, append([]PagerOption{Asc("id")}, tt.opts...)...)
			if p.Size() != tt.size || p.Page() != tt.page || p.Offset() != 0 {
				t.Fatalf("size=%d page=%d offset=%d", p.Size(), p.Page(), p.Offset())
			}
		})
	}
}

func TestCursorPager_TokenFromOtherSort_FirstPage(t *testing.T) {
	tok := cursorFor(t, []PagerOption{Asc("id")}, int64(5))

	p := CursorPager(PageRequest{Cursor: tok}, descKeys...)
	if w, _ := p.Where(Dollar(1)); p.Err() != nil || w != "" {
		t.Fatalf("err=%v where=%q", p.Err(), w)
	}
}

func TestCursorPager_OffsetToken_FirstPage(t *testing.T) {
	tok, _ := encodeOffsetToken(fingerprint([]pagerKey{{column: "id"}}), 40)

	p := CursorPager(PageRequest{Cursor: tok}, Asc("id"))
	if w, _ := p.Where(Dollar(1)); p.Err() != nil || w != "" || p.Offset() != 0 {
		t.Fatalf("err=%v where=%q offset=%d", p.Err(), w, p.Offset())
	}
}

func TestOffsetPager_CursorToken_FirstPage(t *testing.T) {
	tok := cursorFor(t, []PagerOption{Asc("id")}, int64(5))

	p := OffsetPager(PageRequest{Cursor: tok, Page: 4}, Asc("id"))
	if p.Err() != nil || p.Offset() != 0 || p.Page() != 1 {
		t.Fatalf("err=%v offset=%d page=%d", p.Err(), p.Offset(), p.Page())
	}
}

func TestCursorPager_PageParamIgnored(t *testing.T) {
	p := CursorPager(PageRequest{Page: 7}, Asc("id"))
	if p.Offset() != 0 || p.Page() != 1 || p.LimitOffset() != "LIMIT 21" {
		t.Fatalf("offset=%d page=%d %q", p.Offset(), p.Page(), p.LimitOffset())
	}
}

func TestPager_InvalidToken_ErrInvalidCursor(t *testing.T) {
	fp := fingerprint([]pagerKey{{column: "id"}})
	enc := b64

	tests := map[string]string{
		"not base64":   "!!!not-base64!!!",
		"not json":     enc("hello"),
		"v2":           enc(`{"v":2,"m":"o","f":"` + fp + `","offset":1}`),
		"too long":     strings.Repeat("A", 257),
		"extra value":  enc(`{"v":1,"m":"c","f":"` + fp + `","k":["i:1","i:2"]}`),
		"unknown tag":  enc(`{"v":1,"m":"c","f":"` + fp + `","k":["q:1"]}`),
		"unknown mode": enc(`{"v":1,"m":"z","f":"` + fp + `"}`),
		"bad int":      enc(`{"v":1,"m":"c","f":"` + fp + `","k":["i:abc"]}`),
	}

	for name, tok := range tests {
		t.Run(name, func(t *testing.T) {
			pagers := []*Pager{CursorPager(PageRequest{Cursor: tok, Page: 3}, Asc("id"))}
			if name != "extra value" { // the value count is checked only by the cursor approach
				pagers = append(pagers, OffsetPager(PageRequest{Cursor: tok}, Asc("id")))
			}

			for _, p := range pagers {
				if !errors.Is(p.Err(), ErrInvalidCursor) {
					t.Fatalf("err = %v", p.Err())
				}

				if w, _ := p.Where(Dollar(1)); w != "" || p.Offset() != 0 || p.LimitOffset() != "LIMIT 21" {
					t.Fatalf("must be the first page: %q %q", w, p.LimitOffset())
				}
			}
		})
	}
}

// pagerDB is a DB implementation that does not know its paging mode (service mock).
type pagerDB struct{ DB }

func TestNewPager_UsesClientDefault(t *testing.T) {
	conn := &sqlx.DB{}

	tok := cursorFor(t, []PagerOption{Asc("id")}, int64(5))
	req := PageRequest{Cursor: tok, Page: 3}

	cur := NewPager(NewDBWithOptions(conn, WithPaging(PagingCursor)), req, Asc("id"))
	if w, _ := cur.Where(Dollar(1)); w != "(id) > ($1)" {
		t.Fatalf("cursor client: %q", w)
	}

	for name, db := range map[string]DB{"NewDB": NewDB(conn), "foreign": pagerDB{}, "nil": nil} {
		p := NewPager(db, PageRequest{Page: 3}, Asc("id"))
		if p.Offset() != 40 {
			t.Fatalf("%s: want offset pager, offset=%d", name, p.Offset())
		}
	}
}

func TestAllPager_NoLimit(t *testing.T) {
	p := AllPager(Asc("id"), MaxSize(3))
	if p.LimitOffset() != "" || p.Limit() != 0 || p.Err() != nil {
		t.Fatalf("limit %q err %v", p.LimitOffset(), p.Err())
	}

	rows := []int{1, 2, 3, 4, 5}

	res := PageOf(rows, p, nil)
	if len(res.Items) != 5 || res.HasMore || res.NextCursor != nil {
		t.Fatalf("res = %+v", res)
	}

	if AllPager().Err() != nil {
		t.Fatal("AllPager needs no keys")
	}
}

func TestCursorPager_MixedDirections_ErrInvalidPager(t *testing.T) {
	p := CursorPager(PageRequest{}, Desc("created_at"), Asc("id"))
	if !errors.Is(p.Err(), ErrInvalidPager) {
		t.Fatalf("err = %v", p.Err())
	}

	// offset allows mixed directions
	if OffsetPager(PageRequest{}, Desc("created_at"), Asc("id")).Err() != nil {
		t.Fatal("offset must allow mixed directions")
	}
}

func TestPager_BadColumn_ErrInvalidPager(t *testing.T) {
	for _, col := range []string{"id; DROP TABLE x", "a b", "", "1abc", "a.b.c", "id--"} {
		p := OffsetPager(PageRequest{}, Asc(col))
		if !errors.Is(p.Err(), ErrInvalidPager) {
			t.Fatalf("%q: err = %v", col, p.Err())
		}

		if p.OrderBy() != "" || strings.Contains(p.LimitOffset(), "DROP") {
			t.Fatalf("%q: unsafe fragments %q", col, p.OrderBy())
		}
	}

	if !errors.Is(OffsetPager(PageRequest{}, Asc("id"), Desc("ID")).Err(), ErrInvalidPager) {
		t.Fatal("duplicate column")
	}
}

func TestPager_NoKeys_ErrInvalidPager(t *testing.T) {
	if !errors.Is(OffsetPager(PageRequest{}).Err(), ErrInvalidPager) ||
		!errors.Is(CursorPager(PageRequest{}).Err(), ErrInvalidPager) {
		t.Fatal("want ErrInvalidPager")
	}
}

func b64(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
