// Package sqlbpager applies a sqlkit.Pager to a go-sqlbuilder SelectBuilder.
//
// It lives in a subpackage so that the root sqlkit package does not depend on go-sqlbuilder.
package sqlbpager

import (
	"github.com/huandu/go-sqlbuilder"

	"github.com/boostgo/sqlkit"
)

// Apply adds the keyset condition, ORDER BY, LIMIT (size+1) and OFFSET of p to sb.
//
// Note: sb.OrderBy of go-sqlbuilder APPENDS columns, while sb.Asc()/sb.Desc() set the direction of the
// whole list; do not call them together with Apply. Check p.Err() before executing the query.
func Apply(sb *sqlbuilder.SelectBuilder, p *sqlkit.Pager) {
	if where, _ := p.Where(sb.Var); where != "" {
		sb.Where(where)
	}

	if cols := p.OrderByColumns(); len(cols) > 0 {
		sb.OrderBy(cols...)
	}

	if limit := p.Limit(); limit > 0 {
		sb.Limit(limit)
	}

	if offset := p.Offset(); offset > 0 {
		sb.Offset(offset)
	}
}
