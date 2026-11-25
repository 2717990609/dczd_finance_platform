package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/2717990609/dczd_finance_platform/internal/director"
	"github.com/jackc/pgx/v5"
)

// A legacy file already carries confirmed party and source identifiers. Never
// resolve its parties by display name or apply editable workbook suggestions.
func directorLegacyRows(ctx context.Context, tx pgx.Tx, p directorPeriod, input directorEdit) ([]director.Row, []director.Issue, int, error) {
	var original directorDocument
	if len(input.File) == 0 || len(input.File) > 10<<20 || len(input.FileName) > 255 || filepath.Base(input.FileName) != input.FileName ||
		!strings.EqualFold(filepath.Ext(input.FileName), ".json") || len(input.Bindings) != 0 ||
		json.Unmarshal(input.File, &original) != nil || original.Version < 1 || original.PeriodKey != p.Key || !p.Start.Equal(original.StartsAt) || len(original.Rows) == 0 || len(original.Rows) > 5000 {
		return nil, nil, 0, fmt.Errorf("%w：请选择与原核定版本期次及开班时间一致的家庭教育期次，并上传完整 JSON", invalid)
	}
	var legacyIDs, sourceIDs, companyIDs, personIDs []string
	for _, r := range original.Rows {
		legacyIDs = append(legacyIDs, r.LeaderID)
		sourceIDs = append(sourceIDs, r.LeaderSourceCompanyID, r.MemberSourceCompanyID)
		companyIDs = append(companyIDs, r.LeaderCompanyID, r.MemberCompanyID, r.PendingCompanyID)
		personIDs = append(personIDs, r.LeaderSourceUserID)
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT s.company_id::text FROM organization_company_sources s WHERE s.source='DCZD' AND s.source_company_id=ANY($1::text[])`, sourceIDs)
	if err != nil {
		return nil, nil, 0, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, 0, err
	}
	// A2 company party IDs and the new directory's membership-unit IDs can
	// differ. Prefer the verified migration key, including pending rows which
	// do not yet have a usable business-system company number.
	rows, err = tx.Query(ctx, `SELECT wire_id::text,unit_id::text FROM organization_dispatch_company_keys
	WHERE wire_id::text=ANY($1::text[])`, companyIDs)
	if err != nil {
		return nil, nil, 0, err
	}
	transferred := map[string]string{}
	var wire, unit string
	_, err = pgx.ForEachRow(rows, []any{&wire, &unit}, func() error { transferred[wire] = unit; found = append(found, unit); return nil })
	if err != nil {
		return nil, nil, 0, err
	}
	d, err := readDirectorDirectory(ctx, tx, p.Start, nil, append(companyIDs, found...))
	if err != nil {
		return nil, nil, 0, err
	}
	rows, err = tx.Query(ctx, `SELECT id::text,coalesce(legacy_party_id::text,'') FROM people WHERE legacy_party_id::text=ANY($1::text[])
 OR EXISTS(SELECT 1 FROM identity_mappings m WHERE m.person_id=people.id AND m.source='DCZD' AND m.external_id=ANY($2::text[]))`, legacyIDs, personIDs)
	if err != nil {
		return nil, nil, 0, err
	}
	people := map[string]string{}
	var id, legacy string
	_, err = pgx.ForEachRow(rows, []any{&id, &legacy}, func() error {
		if legacy != "" {
			people[legacy] = id
		}
		return nil
	})
	if err != nil {
		return nil, nil, 0, err
	}

	companies := map[string][]directorCompany{}
	for _, c := range d.companies {
		if c.Source != "" {
			companies[c.Source] = append(companies[c.Source], c)
		}
	}
	resolve := func(id, key string) directorCompany {
		if local, known := transferred[id]; known {
			c := d.companies[local]
			if c.Source == key && key != "" {
				return c
			}
			return directorCompany{}
		}
		if id != "" {
			if c, found := d.companies[id]; found {
				if c.Source == key && key != "" {
					return c
				}
				return directorCompany{}
			}
		}
		if options := companies[key]; len(options) == 1 {
			return options[0]
		}
		return directorCompany{}
	}
	issues := []director.Issue{}
	for i := range original.Rows {
		r := &original.Rows[i]
		issue := func(msg string) { issues = append(issues, director.Issue{Number: r.Number, Detail: msg}) }
		if r.Number < 1 {
			issue("原表序号须为正整数")
		}
		oldPerson, oldCompany, oldPayee := r.LeaderID, r.LeaderCompanyID, r.RecipientID
		if oldPerson == "" || oldCompany == "" || (oldPayee != oldPerson && oldPayee != oldCompany) {
			issue("原核定档案缺少总监本人、公司或收款方")
		}
		c := resolve(oldCompany, r.LeaderSourceCompanyID)
		var a directorPerson
		for _, candidate := range d.accounts[c.ID] {
			if candidate.Source != r.LeaderSourceUserID || candidate.Source == "" {
				continue
			}
			if expected := people[oldPerson]; expected != "" && candidate.ID != expected {
				continue
			}
			if a.ID != "" {
				a = directorPerson{}
				break
			}
			a = candidate
		}
		if a.ID == "" {
			issue("总监之道账号、历史财务档案或开班时公司成员关系未能唯一对应")
		}
		recipient, label := directorPayment(c, a)
		if (oldPayee == oldPerson && recipient != a.ID) || (oldPayee == oldCompany && recipient != c.ID) || recipient == "" {
			issue("原核定收款方与公司类型或个人例外不符")
		}
		r.LeaderID, r.LeaderDisplayName, r.LeaderCompanyID, r.LeaderCompanyDisplayName = a.ID, a.Name, c.ID, c.Name
		r.RecipientID, r.RecipientDisplayName = recipient, label
		if r.LeaderName == "" {
			r.LeaderName = a.Name
		}
		if r.LeaderCompanyName == "" {
			r.LeaderCompanyName = c.Name
		}
		if r.PendingCompanyID != "" {
			if local, found := transferred[r.PendingCompanyID]; found {
				r.PendingCompanyID = local
			}
			if _, ok := d.companies[r.PendingCompanyID]; !ok {
				issue("待核对公司的财务档案未导入")
			}
		}
		if r.DirectorOnly || r.PendingReview {
			continue
		}
		member := resolve(r.MemberCompanyID, r.MemberSourceCompanyID)
		r.MemberCompanyID, r.MemberCompanyDisplayName = member.ID, member.Name
		if r.MemberCompanyName == "" {
			r.MemberCompanyName = member.Name
		}
	}
	return original.Rows, issues, original.Version, nil
}
