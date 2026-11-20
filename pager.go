package sqlkit

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	defaultPageSize = 20
	maxPageSize     = 1000
)

var columnRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// PagingMode is the default pagination approach of a client, see WithPaging.
type PagingMode int

const (
	// PagingOffset is the default approach: LIMIT/OFFSET.
	PagingOffset PagingMode = iota
	// PagingCursor is keyset pagination.
	PagingCursor
)

// pagingModer is implemented by clients that know their default paging mode.
type pagingModer interface {
	pagingMode() PagingMode
}

// PageRequest is the pagination input of a request. Zero values mean defaults.
type PageRequest struct {
	Page   int    // 1-based page number (offset approach only)
	Size   int    // page size; <= 0 means 20
	Cursor string // token returned as PageResult.NextCursor
}

type pagerKey struct {
	column string
	desc   bool
}

type pagerConfig struct {
	keys    []pagerKey
	maxSize int
}

// PagerOption is Asc, Desc or MaxSize.
type PagerOption func(*pagerConfig)

// Asc adds an ascending sort key. The order of options is the order of sorting.
//
// The last key must be unique (e.g. id): the library can not check it. Column is
// "name" or "table.name"; anything else makes Pager.Err return ErrInvalidPager.
// Keys of CursorPager must be NOT NULL: a tuple comparison with NULL yields an empty page.
func Asc(column string) PagerOption {
	return func(c *pagerConfig) { c.keys = append(c.keys, pagerKey{column: column}) }
}

// Desc adds a descending sort key; see Asc for the rules.
func Desc(column string) PagerOption {
	return func(c *pagerConfig) { c.keys = append(c.keys, pagerKey{column: column, desc: true}) }
}

// MaxSize sets the maximum page size (n > 0; default 1000). Ignored by AllPager.
func MaxSize(n int) PagerOption {
	return func(c *pagerConfig) { c.maxSize = n }
}

type pagerMode int

const (
	pagerOffset pagerMode = iota
	pagerCursor
	pagerAll
)

// Pager describes one page as SQL fragments and arguments and parses an already fetched slice (PageOf).
// It never executes SQL. Build it with NewPager, OffsetPager, CursorPager or AllPager.
//
// On Err() != nil all fragments describe the first page, so ignoring the error is safe.
type Pager struct {
	mode   pagerMode
	keys   []pagerKey
	fp     string
	size   int
	page   int
	offset int
	cursor []any // decoded keyset values of the next page; nil for the first page
	err    error
}

// NewPager builds an OffsetPager or a CursorPager depending on the default mode of db
// (see WithPaging). A db that does not know its mode (shard client, service mocks) means offset.
func NewPager(db DB, req PageRequest, opts ...PagerOption) *Pager {
	if m, ok := db.(pagingModer); ok && m.pagingMode() == PagingCursor {
		return CursorPager(req, opts...)
	}

	return OffsetPager(req, opts...)
}

// OffsetPager builds an offset (LIMIT/OFFSET) pager.
//
// A non-empty req.Cursor wins over req.Page; a token of the cursor approach or of another sort
// order means the first page. A broken token gives ErrInvalidCursor.
func OffsetPager(req PageRequest, opts ...PagerOption) *Pager {
	p := newPager(pagerOffset, req, opts)

	if req.Cursor != "" {
		p.applyToken(req.Cursor)
	}

	return p
}

// CursorPager builds a keyset pager. req.Page is ignored; without a cursor it is the first page.
//
// All keys must have the same direction, the last key must be unique and all keys NOT NULL.
// A token of the offset approach or of another sort order means the first page (never "all").
func CursorPager(req PageRequest, opts ...PagerOption) *Pager {
	p := newPager(pagerCursor, req, opts)
	p.resetToFirstPage()

	if p.err == nil && len(p.keys) > 0 {
		for _, k := range p.keys[1:] {
			if k.desc != p.keys[0].desc {
				p.invalid("cursor pager keys must have the same direction")

				break
			}
		}
	}

	if req.Cursor != "" {
		p.applyToken(req.Cursor)
	}

	return p
}

// AllPager returns a pager without LIMIT: for internal snapshots only, never for user input.
// Size and MaxSize are ignored; sort keys are optional.
func AllPager(opts ...PagerOption) *Pager {
	return newPager(pagerAll, PageRequest{}, opts)
}

func newPager(mode pagerMode, req PageRequest, opts []PagerOption) *Pager {
	var cfg pagerConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	p := &Pager{mode: mode, keys: cfg.keys, page: req.Page}
	p.fp = fingerprint(p.keys)

	p.validateKeys()

	if mode == pagerAll {
		p.page = 1

		return p
	}

	maxSize := maxPageSize
	if cfg.maxSize > 0 {
		maxSize = cfg.maxSize
	}

	p.size = req.Size
	if p.size <= 0 {
		p.size = defaultPageSize
	}

	if p.size > maxSize {
		p.size = maxSize
	}

	if p.page <= 0 {
		p.page = 1
	}

	p.offset = (p.page - 1) * p.size

	return p
}

func (p *Pager) invalid(reason string) {
	if p.err == nil || !errors.Is(p.err, ErrInvalidPager) {
		p.err = fmt.Errorf("%w: %s", ErrInvalidPager, reason)
	}

	p.resetToFirstPage()
}

func (p *Pager) resetToFirstPage() {
	p.page = 1
	p.offset = 0
	p.cursor = nil
}

func (p *Pager) validateKeys() {
	if len(p.keys) == 0 && p.mode != pagerAll {
		p.invalid("no sort keys")

		return
	}

	seen := make(map[string]struct{}, len(p.keys))

	for _, k := range p.keys {
		if !columnRe.MatchString(k.column) {
			p.invalid("bad column name " + strconv.Quote(k.column))

			return
		}

		lower := strings.ToLower(k.column)
		if _, dup := seen[lower]; dup {
			p.invalid("duplicate column " + k.column)

			return
		}

		seen[lower] = struct{}{}
	}
}

// applyToken applies the page token; mismatched approach/sort order means the first page.
func (p *Pager) applyToken(cursor string) {
	if p.err != nil {
		return
	}

	t, err := decodeToken(cursor)
	if err != nil {
		p.err = err
		p.resetToFirstPage()

		return
	}

	want := tokenModeOffset
	if p.mode == pagerCursor {
		want = tokenModeCursor
	}

	if t.M != want || t.F != p.fp {
		p.resetToFirstPage() // another approach or sort order: first page, Page is ignored

		return
	}

	if p.mode == pagerOffset {
		p.offset = *t.Offset
		p.page = p.offset/p.size + 1

		return
	}

	if len(t.values) != len(p.keys) {
		p.err = fmt.Errorf("%w: wrong number of values", ErrInvalidCursor)
		p.resetToFirstPage()

		return
	}

	p.cursor = t.values
}

// Err returns ErrInvalidCursor (broken token: a client error, e.g. HTTP 400) or ErrInvalidPager
// (programmer error, e.g. HTTP 500).
func (p *Pager) Err() error { return p.err }

// Where returns the keyset condition and its arguments, e.g. "(created_at, id) < ($3, $4)".
// ph is called once per value, in order, and returns its placeholder; if ph registers the values
// itself (sb.Var, Arguments), the returned args can be ignored.
//
// For offset and for the first page of a cursor pager it returns "", nil.
func (p *Pager) Where(ph func(v any) string) (string, []any) {
	if p.mode != pagerCursor || p.cursor == nil || p.err != nil {
		return "", nil
	}

	cols := make([]string, len(p.keys))
	phs := make([]string, len(p.keys))

	for i, k := range p.keys {
		cols[i] = k.column
		phs[i] = ph(p.cursor[i])
	}

	op := ">"
	if p.keys[0].desc {
		op = "<"
	}

	args := append([]any(nil), p.cursor...)

	return "(" + strings.Join(cols, ", ") + ") " + op + " (" + strings.Join(phs, ", ") + ")", args
}

// OrderByColumns returns ["created_at DESC", "id DESC"]; nil if the config is invalid.
func (p *Pager) OrderByColumns() []string {
	if errors.Is(p.err, ErrInvalidPager) {
		return nil
	}

	cols := make([]string, len(p.keys))
	for i, k := range p.keys {
		cols[i] = k.column + " ASC"
		if k.desc {
			cols[i] = k.column + " DESC"
		}
	}

	return cols
}

// OrderBy returns "ORDER BY created_at DESC, id DESC"; "" without keys.
func (p *Pager) OrderBy() string {
	cols := p.OrderByColumns()
	if len(cols) == 0 {
		return ""
	}

	return "ORDER BY " + strings.Join(cols, ", ")
}

// LimitOffset returns "LIMIT 51" or "LIMIT 51 OFFSET 100": the limit is size+1 so PageOf can
// tell whether there is a next page; OFFSET is omitted at 0. AllPager returns "".
func (p *Pager) LimitOffset() string {
	if p.mode == pagerAll {
		return ""
	}

	s := "LIMIT " + strconv.Itoa(p.Limit())
	if p.offset > 0 {
		s += " OFFSET " + strconv.Itoa(p.offset)
	}

	return s
}

// Limit is size+1 (0 for AllPager).
func (p *Pager) Limit() int {
	if p.mode == pagerAll {
		return 0
	}

	return p.size + 1
}

// Offset is the number of rows to skip (0 for cursor and AllPager).
func (p *Pager) Offset() int { return p.offset }

// Size is the normalized page size (0 for AllPager).
func (p *Pager) Size() int { return p.size }

// Page is the 1-based page number of the offset approach, for Total/MaxPages that the service
// counts with a separate COUNT query. It is always 1 for cursor and AllPager.
func (p *Pager) Page() int { return p.page }

// PageResult is a parsed page. NextCursor is nil on the last page.
type PageResult[T any] struct {
	Items      []T
	HasMore    bool
	NextCursor *string
}

// PageOf trims the rows fetched with LimitOffset (size+1) to a page.
//
// For a cursor pager key must return the sort key values of a row, in key order; a nil key or a
// wrong number of values panics (programmer error). A key that does not fit into a 256 byte token
// (long string keys) also panics: a token the decoder would reject is never returned.
// For an offset pager key is not called. AllPager returns all rows.
func PageOf[T any](rows []T, p *Pager, key func(T) []any) PageResult[T] {
	if p.mode == pagerAll || len(rows) <= p.size {
		return PageResult[T]{Items: rows}
	}

	items := rows[:p.size]

	var (
		token string
		err   error
	)

	if p.mode == pagerCursor {
		if key == nil {
			panic("sqlkit.PageOf: key func is required for a cursor pager")
		}

		values := key(items[len(items)-1])
		if len(values) != len(p.keys) {
			panic(fmt.Sprintf("sqlkit.PageOf: key returned %d values, pager has %d keys", len(values), len(p.keys)))
		}

		token, err = encodeCursorToken(p.fp, values)
	} else {
		token, err = encodeOffsetToken(p.fp, p.offset+p.size)
	}

	if err != nil {
		panic("sqlkit.PageOf: can not build next cursor: " + err.Error())
	}

	return PageResult[T]{Items: items, HasMore: true, NextCursor: &token}
}
