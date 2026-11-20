package sqlbpager_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/huandu/go-sqlbuilder"
	"go.uber.org/goleak"

	"github.com/boostgo/sqlkit"
	"github.com/boostgo/sqlkit/sqlbpager"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func cursorToken(t *testing.T) string {
	t.Helper()

	type row struct {
		ts time.Time
		id int64
	}

	rows := []row{{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 42}, {}}
	p := sqlkit.CursorPager(sqlkit.PageRequest{Size: 1}, sqlkit.Desc("created_at"), sqlkit.Desc("id"))

	res := sqlkit.PageOf(rows, p, func(r row) []any { return []any{r.ts, r.id} })
	if res.NextCursor == nil {
		t.Fatal("no cursor")
	}

	return *res.NextCursor
}

func TestApply_SelectBuilder(t *testing.T) {
	p := sqlkit.CursorPager(sqlkit.PageRequest{Size: 10, Cursor: cursorToken(t)}, sqlkit.Desc("created_at"), sqlkit.Desc("id"))

	sb := sqlbuilder.NewSelectBuilder()
	sb.Select("id").From("t")
	sb.Where(sb.Equal("tenant", "acme"))
	sqlbpager.Apply(sb, p)

	query, args := sb.BuildWithFlavor(sqlbuilder.PostgreSQL)

	want := "SELECT id FROM t WHERE tenant = $1 AND (created_at, id) < ($2, $3) ORDER BY created_at DESC, id DESC LIMIT $4"
	if query != want {
		t.Fatalf("query = %q", query)
	}

	if len(args) != 4 || args[0] != "acme" || args[2] != int64(42) || args[3] != 11 {
		t.Fatalf("args = %v", args)
	}

	// offset pager: LIMIT + OFFSET, no WHERE from the pager
	sb = sqlbuilder.NewSelectBuilder()
	sb.Select("id").From("t")
	sqlbpager.Apply(sb, sqlkit.OffsetPager(sqlkit.PageRequest{Page: 2, Size: 10}, sqlkit.Asc("id")))

	query, _ = sb.BuildWithFlavor(sqlbuilder.PostgreSQL)
	if want = "SELECT id FROM t ORDER BY id ASC LIMIT $1 OFFSET $2"; query != want {
		t.Fatalf("query = %q", query)
	}
}

func TestRootPackage_NoSqlbuilderDep(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "..").CombinedOutput()
	if err != nil {
		t.Skipf("go list: %v", err)
	}

	if strings.Contains(string(out), "huandu/go-sqlbuilder") {
		t.Fatal("root package must not import go-sqlbuilder")
	}
}
