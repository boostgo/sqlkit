package sqlkit

import (
	"bytes"
	"database/sql/driver"
	"testing"
	"time"
)

type testUUID [16]byte

func (u testUUID) Value() (driver.Value, error) { return "123e4567-e89b-12d3-a456-426614174000", nil }

func TestCursorToken_RoundTripTypes(t *testing.T) {
	loc := time.FixedZone("", 5*3600)
	ts := time.Date(2026, 11, 11, 10, 0, 0, 123456789, loc)

	in := []any{
		int64(9007199254740993), // 2^53 + 1: must not go through float64
		ts,
		"hello, wörld: 世界",
		[]byte{0, 1, 2, 255},
		testUUID{},
		true,
		1.5,
		nil,
	}

	tok, err := encodeCursorToken("fp", in)
	if err != nil {
		t.Fatal(err)
	}

	got, err := decodeToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	v := got.values
	if v[0] != int64(9007199254740993) {
		t.Errorf("int64 lost precision: %v", v[0])
	}

	if gt := v[1].(time.Time); !gt.Equal(ts) {
		t.Errorf("time = %v", gt)
	} else if _, off := gt.Zone(); off != 5*3600 {
		t.Errorf("offset lost: %v", off)
	}

	if v[2] != "hello, wörld: 世界" {
		t.Errorf("string = %v", v[2])
	}

	if !bytes.Equal(v[3].([]byte), []byte{0, 1, 2, 255}) {
		t.Errorf("bytes = %v", v[3])
	}

	if v[4] != "123e4567-e89b-12d3-a456-426614174000" {
		t.Errorf("valuer = %v", v[4])
	}

	if v[5] != true || v[6] != 1.5 || v[7] != nil {
		t.Errorf("bool/float/nil = %v %v %v", v[5], v[6], v[7])
	}
}

func TestOffsetToken_NextPage(t *testing.T) {
	fp := fingerprint([]pagerKey{{column: "id"}})

	tok, err := encodeOffsetToken(fp, 100)
	if err != nil {
		t.Fatal(err)
	}

	p := OffsetPager(PageRequest{Cursor: tok, Size: 50}, Asc("id"))
	if p.Err() != nil || p.Offset() != 100 || p.Page() != 3 || p.LimitOffset() != "LIMIT 51 OFFSET 100" {
		t.Fatalf("err=%v offset=%d page=%d", p.Err(), p.Offset(), p.Page())
	}

	// a token of another sort order is the first page
	if o := OffsetPager(PageRequest{Cursor: tok}, Desc("id")); o.Offset() != 0 || o.Err() != nil {
		t.Fatalf("other sort: offset=%d err=%v", o.Offset(), o.Err())
	}
}

func BenchmarkCursorToken_PG3(b *testing.B) {
	vals := []any{time.Now(), int64(42)}

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tok, _ := encodeCursorToken("abcdef01", vals)
		_, _ = decodeToken(tok)
	}
}
