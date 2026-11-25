package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/2717990609/dczd_finance_platform/internal/director"
	"github.com/jackc/pgx/v5"
)

// At the opening instant prefer the effective source identifier. If the record
// predates the imported history, only an unambiguous current/historical key can bind.
func directorUniqueSourceSQL(table, party, sourceKey, target string) string {
	// Check both directions: an old key of this party can now belong to a
	// different party at opening. Such a key must never expand the payout.
	values := func(column string) string {
		return fmt.Sprintf(`coalesce(
 array_agg(DISTINCT %[1]s) FILTER(WHERE valid_from<=$1 AND (valid_to IS NULL OR $1<valid_to)),
 array_agg(DISTINCT %[1]s) FILTER(WHERE valid_from<=CURRENT_TIMESTAMP AND (valid_to IS NULL OR CURRENT_TIMESTAMP<valid_to)),
 array_agg(DISTINCT %[1]s))`, column)
	}
	return fmt.Sprintf(` LEFT JOIN LATERAL (
 SELECT forward.ids[1] AS source_key FROM (SELECT %s AS ids FROM %s WHERE source='DCZD' AND %s=%s) forward
 CROSS JOIN LATERAL (SELECT %s AS ids FROM %s WHERE source='DCZD' AND %s=forward.ids[1]) reverse
 WHERE cardinality(forward.ids)=1 AND cardinality(reverse.ids)=1 AND reverse.ids[1]=%s) keys ON true `,
		values(sourceKey), table, party, target, values(party), table, sourceKey, target)
}

var directorCompanyDirectory = `SELECT u.id::text,u.name,u.sales_company_type,u.status,coalesce(cat.name,''),coalesce(keys.source_key,'')
 FROM organization_units u LEFT JOIN LATERAL (
 SELECT c.name FROM organization_sales_category_history h JOIN organization_sales_categories c ON c.id=h.category_id
 WHERE h.unit_id=u.id AND h.valid_from<=CURRENT_TIMESTAMP AND (h.valid_to IS NULL OR h.valid_to>CURRENT_TIMESTAMP) ORDER BY h.valid_from DESC LIMIT 1
 ) cat ON true` + directorUniqueSourceSQL("organization_company_sources", "company_id", "source_company_id", "u.id") + `
 WHERE u.sales_company_type IN ('INTERNAL','EXTERNAL')`

var directorAccountDirectory = `SELECT p.id::text,p.name,a.unit_id::text,bool_or(a.kind IN ('OWNER','MANAGER')),coalesce(keys.source_key,'')
 FROM organization_assignments a JOIN people p ON p.id=a.person_id ` + directorUniqueSourceSQL("identity_mappings", "person_id", "external_id", "p.id") + `
 WHERE a.kind IN ('OWNER','MANAGER','MEMBER') AND a.valid_from<=$1 AND (a.valid_to IS NULL OR $1<a.valid_to)
 AND a.unit_id::text=ANY($2::text[]) GROUP BY p.id,p.name,a.unit_id,keys.source_key ORDER BY p.name,p.id`

type directorCompany struct{ ID, Name, Type, Status, Category, Source string }
type directorPerson struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Company string `json:"companyId"`
	Manager bool   `json:"-"`
	Source  string `json:"sourceId"`
}
type directorDirectory struct {
	companies map[string]directorCompany
	names     map[string][]string
	accounts  map[string][]directorPerson
}
type directorBinding struct {
	Number  int    `json:"number"`
	Leader  string `json:"leaderId"`
	Company string `json:"companyId"`
	Member  string `json:"memberId"`
	Pending bool   `json:"pending"`
}

func directorSourceKey(id string) string {
	if positiveSourceID(id) > 0 {
		return id
	}
	return ""
}

// Original sheets often put the representative's name in parentheses. Use
// the same conservative company comparison as A2 and require a unique result.
func comparableDirectorCompany(name string) string {
	var out strings.Builder
	depth := 0
	nonDigit := false
	for _, r := range strings.ToLower(name) {
		switch r {
		case '(', '（':
			depth++
			continue
		case ')', '）':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 || unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		out.WriteRune(r)
		nonDigit = nonDigit || !unicode.IsDigit(r)
	}
	key := out.String()
	if len([]rune(key)) < 2 || !nonDigit {
		return ""
	}
	return key
}

func resolveDirectorCompanyNames(ctx context.Context, tx pgx.Tx, names []string) (map[string][]string, []string, error) {
	matches := map[string][]string{}
	if len(names) == 0 {
		return matches, nil, nil
	}
	wanted := map[string]string{}
	normalized := map[string][]string{}
	for _, name := range names {
		wanted[name] = comparableDirectorCompany(name)
	}
	for _, key := range wanted {
		if key != "" {
			normalized[key] = nil
		}
	}
	rows, err := tx.Query(ctx, `SELECT u.id::text,u.name FROM organization_units u WHERE u.sales_company_type IN ('INTERNAL','EXTERNAL')
 AND EXISTS(SELECT 1 FROM organization_company_sources s WHERE s.company_id=u.id AND s.source='DCZD')`)
	if err != nil {
		return nil, nil, err
	}
	var id, name string
	_, err = pgx.ForEachRow(rows, []any{&id, &name}, func() error {
		if _, ok := wanted[name]; ok {
			matches[name] = append(matches[name], id)
		}
		key := comparableDirectorCompany(name)
		if _, ok := normalized[key]; ok {
			normalized[key] = append(normalized[key], id)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	ids := []string{}
	for name, key := range wanted {
		if len(matches[name]) == 0 {
			matches[name] = normalized[key]
		}
		if len(matches[name]) == 1 {
			ids = append(ids, matches[name][0])
		}
	}
	return matches, ids, nil
}

func readDirectorDirectory(ctx context.Context, tx pgx.Tx, at time.Time, names, ids []string) (directorDirectory, error) {
	d := directorDirectory{map[string]directorCompany{}, map[string][]string{}, map[string][]directorPerson{}}
	matches, matched, err := resolveDirectorCompanyNames(ctx, tx, names)
	if err != nil {
		return d, err
	}
	d.names = matches
	rows, err := tx.Query(ctx, directorCompanyDirectory+` AND u.id::text=ANY($2::text[])`, at, append(ids, matched...))
	if err != nil {
		return d, err
	}
	var c directorCompany
	_, err = pgx.ForEachRow(rows, []any{&c.ID, &c.Name, &c.Type, &c.Status, &c.Category, &c.Source}, func() error {
		c.Source = directorSourceKey(c.Source)
		d.companies[c.ID] = c

		return nil
	})
	if err != nil {
		return d, err
	}
	var companies []string
	for id := range d.companies {
		companies = append(companies, id)
	}
	rows, err = tx.Query(ctx, directorAccountDirectory, at, companies)
	if err != nil {
		return d, err
	}
	var a directorPerson
	_, err = pgx.ForEachRow(rows, []any{&a.ID, &a.Name, &a.Company, &a.Manager, &a.Source}, func() error {
		a.Source = directorSourceKey(a.Source)
		d.accounts[a.Company] = append(d.accounts[a.Company], a)
		return nil
	})
	return d, err
}
func (d directorDirectory) autoCompany(name string) string {
	if ids := d.names[name]; len(ids) == 1 {
		return ids[0]
	}
	return ""
}
func (d directorDirectory) person(company, id, name string) directorPerson {
	accounts := d.accounts[company]
	if id != "" {
		for _, a := range accounts {
			if a.ID == id {
				return a
			}
		}
		return directorPerson{}
	}
	if len(accounts) == 1 {
		return accounts[0]
	}
	var named, managers []directorPerson
	for _, a := range accounts {
		if a.Name == name {
			named = append(named, a)
		}
		if a.Manager {
			managers = append(managers, a)
		}
	}
	if len(named) == 1 {
		return named[0]
	}
	if len(named) == 0 && len(managers) == 1 {
		return managers[0]
	}
	return directorPerson{}
}

func directorPayment(company directorCompany, person directorPerson) (string, string) {
	if person.Source == "" || company.Source == "" {
		return "", ""
	}
	if company.Type == "EXTERNAL" {
		return company.ID, company.Name
	}
	if company.Type == "INTERNAL" && person.Source == "8504698" {
		return person.ID, person.Name
	}
	return "", ""
}

func (d directorDirectory) bind(row director.Row, b *directorBinding) director.Row {
	companyID, memberID := d.autoCompany(row.LeaderCompanyName), d.autoCompany(row.MemberCompanyName)
	personID := ""
	if b != nil {
		companyID, memberID, personID, row.PendingReview = b.Company, b.Member, b.Leader, b.Pending
	}
	company, member := d.companies[companyID], d.companies[memberID]
	person := directorPerson{}
	if b == nil || personID != "" {
		person = d.person(company.ID, personID, row.LeaderName)
	}
	row.LeaderCompanyID, row.LeaderCompanyDisplayName, row.LeaderSourceCompanyID = company.ID, company.Name, company.Source
	row.MemberCompanyID, row.MemberCompanyDisplayName, row.MemberSourceCompanyID = member.ID, member.Name, member.Source
	row.LeaderID, row.LeaderDisplayName, row.LeaderSourceUserID = person.ID, person.Name, person.Source
	row.RecipientID, row.RecipientDisplayName = directorPayment(company, person)
	return row
}

func (s workspace) directorOptions(w http.ResponseWriter, r *http.Request) {
	ctx, q := r.Context(), r.URL.Query()
	period, err := strconv.ParseInt(q.Get("periodId"), 10, 64)
	if err != nil || period < 1 {
		respond(w, nil, invalid)
		return
	}
	var result any
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		p, err := directorPeriodFor(ctx, tx, period)
		if err != nil {
			return err
		}
		switch q.Get("kind") {
		case "company":
			name := strings.TrimSpace(q.Get("name"))
			if len([]rune(name)) > 100 {
				return invalid
			}
			rows, err := tx.Query(ctx, directorCompanyDirectory+` AND (strpos(lower(u.name),lower($2))>0 OR u.id::text=$2) ORDER BY u.name,u.id LIMIT 100`, p.Start, name)
			if err != nil {
				return err
			}
			type option struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Category string `json:"category"`
				Source   string `json:"sourceId"`
			}
			items := []option{}
			var c directorCompany
			_, err = pgx.ForEachRow(rows, []any{&c.ID, &c.Name, &c.Type, &c.Status, &c.Category, &c.Source}, func() error { items = append(items, option{c.ID, c.Name, c.Category, c.Source}); return nil })
			result = items
			return err
		case "person":
			company, ok := canonicalSourceUUID(q.Get("companyId"))
			if !ok {
				return invalid
			}
			d, err := readDirectorDirectory(ctx, tx, p.Start, nil, []string{company})
			if err != nil {
				return err
			}
			accounts := []directorPerson{}
			for _, a := range d.accounts[company] {
				if id, _ := directorPayment(d.companies[company], a); id != "" {
					accounts = append(accounts, a)
				}
			}
			result = accounts
			return nil
		}
		return invalid
	})
	respond(w, result, err)
}
