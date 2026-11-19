package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// tenantMarker is the predicate every scoped statement must contain; it
// expands to "(org = ? AND owner = ?)" bound from the handle's tenant, so a
// caller can never pass another tenant's values.
const tenantMarker = "{tenant}"

// Tenant owns rows: the organization and the principal inside it.
type Tenant struct {
	Org   string
	Owner string
}

func (t Tenant) valid() bool {
	ok := func(v string) bool { return v != "" && len(v) <= 256 && !strings.ContainsAny(v, "\x00\r\n") }
	return ok(t.Org) && ok(t.Owner)
}

// Scoped executes statements for exactly one tenant inside a transaction.
type Scoped struct {
	tx     *Tx
	tenant Tenant
}

// Scoped binds tenant to the transaction. An incomplete tenant is refused at
// the first statement.
func (tx *Tx) Scoped(tenant Tenant) Scoped { return Scoped{tx: tx, tenant: tenant} }

// Tenant returns the bound tenant.
func (q Scoped) Tenant() Tenant { return q.tenant }

// Exec runs one statement that contains {tenant}.
func (q Scoped) Exec(query string, args ...any) (sql.Result, error) {
	bound, values, err := bind(q.tx.dialect, query, args, q.tenant, true)
	if err != nil {
		return nil, err
	}
	return q.tx.tx.ExecContext(q.tx.ctx, bound, values...)
}

// Query runs one SELECT that contains {tenant}.
func (q Scoped) Query(query string, args ...any) (*sql.Rows, error) {
	bound, values, err := bind(q.tx.dialect, query, args, q.tenant, true)
	if err != nil {
		return nil, err
	}
	return q.tx.tx.QueryContext(q.tx.ctx, bound, values...)
}

// QueryRow runs one single-row SELECT that contains {tenant}.
func (q Scoped) QueryRow(query string, args ...any) Row {
	bound, values, err := bind(q.tx.dialect, query, args, q.tenant, true)
	if err != nil {
		return Row{err: err}
	}
	return Row{row: q.tx.tx.QueryRowContext(q.tx.ctx, bound, values...)}
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Insert adds one row whose org and owner come from the handle.
func (q Scoped) Insert(table string, columns []string, values ...any) error {
	if !q.tenant.valid() {
		return fmt.Errorf("%w: incomplete tenant", ErrUnscoped)
	}
	if !identifier.MatchString(table) || len(columns) != len(values) {
		return fmt.Errorf("store: insert into %q: invalid table or column count", table)
	}
	for _, column := range columns {
		if column == "org" || column == "owner" {
			return fmt.Errorf("%w: org and owner come from the tenant", ErrUnscoped)
		}
		if !identifier.MatchString(column) {
			return fmt.Errorf("store: insert into %s: invalid column %q", table, column)
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(columns)+2), ", ")
	query := "INSERT INTO " + table + " (org, owner, " + strings.Join(columns, ", ") + ") VALUES (" + placeholders + ")"
	bound, args, err := bind(q.tx.dialect, query, append([]any{q.tenant.Org, q.tenant.Owner}, values...), Tenant{}, false)
	if err != nil {
		return err
	}
	_, err = q.tx.tx.ExecContext(q.tx.ctx, bound, args...)
	return err
}

// bind expands {tenant}, checks that no top-level OR can widen it, counts ?
// placeholders outside string literals against args and renumbers them as $n
// for Postgres.
func bind(dialect Dialect, query string, args []any, tenant Tenant, scoped bool) (string, []any, error) {
	if scoped {
		if !tenant.valid() {
			return "", nil, fmt.Errorf("%w: incomplete tenant", ErrUnscoped)
		}
		if !strings.Contains(query, tenantMarker) {
			return "", nil, fmt.Errorf("%w: missing %s predicate", ErrUnscoped, tenantMarker)
		}
	}
	var out strings.Builder
	values := make([]any, 0, len(args)+2)
	next, depth, markerDepth := 0, 0, -1
	var orDepths []int
	placeholder := func() {
		if dialect == Postgres {
			out.WriteString("$" + strconv.Itoa(len(values)))
		} else {
			out.WriteByte('?')
		}
	}
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			end := literalEnd(query, i)
			out.WriteString(query[i:end])
			i = end - 1
		case c == '(':
			depth++
			out.WriteByte(c)
		case c == ')':
			depth--
			out.WriteByte(c)
		case c == '?':
			if next >= len(args) {
				return "", nil, fmt.Errorf("store: more placeholders than %d arguments", len(args))
			}
			values = append(values, args[next])
			next++
			placeholder()
		case scoped && strings.HasPrefix(query[i:], tenantMarker):
			if markerDepth < 0 || depth < markerDepth {
				markerDepth = depth
			}
			out.WriteString("(org = ")
			values = append(values, tenant.Org)
			placeholder()
			out.WriteString(" AND owner = ")
			values = append(values, tenant.Owner)
			placeholder()
			out.WriteByte(')')
			i += len(tenantMarker) - 1
		case scoped && isWord(query, i, "OR"):
			orDepths = append(orDepths, depth)
			out.WriteString(query[i : i+2])
			i++
		default:
			out.WriteByte(c)
		}
	}
	for _, d := range orDepths {
		if d <= markerDepth {
			return "", nil, fmt.Errorf("%w: OR beside the tenant predicate; parenthesize it", ErrUnscoped)
		}
	}
	if next != len(args) {
		return "", nil, fmt.Errorf("store: %d placeholders for %d arguments", next, len(args))
	}
	return out.String(), values, nil
}

// literalEnd returns the index after the single-quoted literal starting at i;
// a doubled quote inside it is an escaped quote.
func literalEnd(query string, i int) int {
	for j := i + 1; j < len(query); j++ {
		if query[j] == '\'' {
			if j+1 < len(query) && query[j+1] == '\'' {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(query)
}

func isWord(query string, i int, word string) bool {
	if len(query)-i < len(word) || !strings.EqualFold(query[i:i+len(word)], word) {
		return false
	}
	wordChar := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	return (i == 0 || !wordChar(query[i-1])) && (i+len(word) == len(query) || !wordChar(query[i+len(word)]))
}
