package sqlbpager_test

import (
	"fmt"

	"github.com/huandu/go-sqlbuilder"

	"github.com/boostgo/sqlkit"
	"github.com/boostgo/sqlkit/sqlbpager"
)

func ExampleApply() {
	p := sqlkit.OffsetPager(sqlkit.PageRequest{Page: 2, Size: 10}, sqlkit.Asc("id"))

	sb := sqlbuilder.NewSelectBuilder()
	sb.Select("id").From("users")
	sb.Where(sb.Equal("tenant", "acme"))
	sqlbpager.Apply(sb, p)

	query, args := sb.BuildWithFlavor(sqlbuilder.PostgreSQL)
	fmt.Println(query)
	fmt.Println(args)
	// Output:
	// SELECT id FROM users WHERE tenant = $1 ORDER BY id ASC LIMIT $2 OFFSET $3
	// [acme 11 10]
}
