package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type reportOrganization struct {
	Company, Department pgtype.UUID
	Group               *pgtype.UUID `json:",omitempty"`
}

func (o reportOrganization) groupID() pgtype.UUID {
	if o.Group == nil {
		return pgtype.UUID{}
	}
	return *o.Group
}

func (o reportOrganization) present() *reportOrganization {
	if o.Company.Valid || o.Department.Valid || o.Group != nil {
		return &o
	}
	return nil
}

type reportAssignment struct {
	PersonID int64
	Kind     string
	Unit     pgtype.UUID
	From     time.Time
	To       *time.Time
}

type reportUnit struct {
	Kind   string
	Parent pgtype.UUID
}

type companySourceAssignment struct {
	Company pgtype.UUID
	From    time.Time
	To      *time.Time
}

type reportAttribution struct {
	Units       map[pgtype.UUID]reportUnit
	Assignments map[int64][]reportAssignment
	Companies   map[identityKey][]companySourceAssignment
}

func loadReportAttribution(ctx context.Context, tx pgx.Tx, people []int64, payments []paymentInput) (reportAttribution, error) {
	out := reportAttribution{
		Units: make(map[pgtype.UUID]reportUnit), Assignments: make(map[int64][]reportAssignment),
		Companies: make(map[identityKey][]companySourceAssignment),
	}
	rows, err := tx.Query(ctx, `SELECT id,kind,parent_id FROM organization_units`)
	if err != nil {
		return out, err
	}
	var unitID pgtype.UUID
	var unit reportUnit
	_, err = pgx.ForEachRow(rows, []any{&unitID, &unit.Kind, &unit.Parent}, func() error {
		out.Units[unitID] = unit
		return nil
	})
	if err != nil {
		return out, err
	}

	var sources, companyKeys []string
	companySeen := make(map[identityKey]bool)
	for _, payment := range payments {
		f := payment.Fact
		key := identityKey{f.Source, f.SalesCompanyID}
		if key.ExternalID != "" && !companySeen[key] {
			companySeen[key] = true
			sources, companyKeys = append(sources, key.Source), append(companyKeys, key.ExternalID)
		}
	}
	if len(sources) > 0 {
		rows, err = tx.Query(ctx, `SELECT c.source,c.source_company_id,c.company_id,c.valid_from,c.valid_to
		 FROM unnest($1::text[],$2::text[]) AS wanted(source,source_company_id)
		 JOIN organization_company_sources c USING(source,source_company_id)
		 JOIN organization_units u ON u.id=c.company_id AND u.kind='COMPANY'`, sources, companyKeys)
		if err != nil {
			return out, err
		}
		var key identityKey
		var assignment companySourceAssignment
		_, err = pgx.ForEachRow(rows, []any{&key.Source, &key.ExternalID, &assignment.Company, &assignment.From, &assignment.To}, func() error {
			out.Companies[key] = append(out.Companies[key], assignment)
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	if len(people) == 0 {
		return out, nil
	}
	rows, err = tx.Query(ctx, `SELECT person_id,kind,unit_id,valid_from,valid_to
	 FROM organization_assignments WHERE person_id=ANY($1::bigint[])`, people)
	if err != nil {
		return out, err
	}
	var a reportAssignment
	_, err = pgx.ForEachRow(rows, []any{&a.PersonID, &a.Kind, &a.Unit, &a.From, &a.To}, func() error {
		out.Assignments[a.PersonID] = append(out.Assignments[a.PersonID], a)
		return nil
	})
	return out, err
}

type uniqueUnit struct {
	ID        pgtype.UUID
	Ambiguous bool
}

func (u *uniqueUnit) add(id pgtype.UUID) {
	if !id.Valid {
		return
	}
	if u.ID.Valid && u.ID != id {
		u.Ambiguous = true
	}
	u.ID = id
}

func (u uniqueUnit) value() pgtype.UUID {
	if u.Ambiguous {
		return pgtype.UUID{}
	}
	return u.ID
}

func (i reportAttribution) ancestors(id pgtype.UUID) reportOrganization {
	var out reportOrganization
	if id.Valid && i.Units[id].Kind != "COMPANY" && i.Units[id].Kind != "DEPARTMENT" {
		out.Group = &id
	}
	node := id
	for depth := 0; node.Valid && depth < len(i.Units); depth++ {
		unit := i.Units[node]
		switch unit.Kind {
		case "COMPANY":
			out.Company = node
			return out
		case "DEPARTMENT":
			if !out.Department.Valid {
				out.Department = node
			}
		}
		node = unit.Parent
	}
	return out
}

func (i reportAttribution) resolve(person int64, at time.Time, sourceCompany identityKey) reportOrganization {
	var companies, hierarchy, primary, concurrent uniqueUnit
	for _, assignment := range i.Assignments[person] {
		if at.Before(assignment.From) || assignment.To != nil && !at.Before(*assignment.To) {
			continue
		}
		unit := i.ancestors(assignment.Unit)
		if i.Units[assignment.Unit].Kind == "COMPANY" {
			companies.add(unit.Company)
		} else {
			hierarchy.add(unit.Company)
		}
		if assignment.Kind == "PRIMARY" {
			primary.add(unit.Department)
		} else {
			concurrent.add(unit.Department)
		}
	}
	if !primary.ID.Valid {
		primary = concurrent
	}
	var company pgtype.UUID
	switch {
	case sourceCompany.ExternalID != "":
		var exact uniqueUnit
		for _, assignment := range i.Companies[sourceCompany] {
			if !at.Before(assignment.From) && (assignment.To == nil || at.Before(*assignment.To)) {
				exact.add(assignment.Company)
			}
		}
		company = exact.value()
	case companies.ID.Valid:
		company = companies.value()
	default:
		company = hierarchy.value()
	}
	department := primary.value()
	if company.Valid {
		if departmentCompany := i.ancestors(department).Company; departmentCompany.Valid && departmentCompany != company {
			department = pgtype.UUID{}
		}
	}
	var primaryGroup, concurrentGroup uniqueUnit
	for _, assignment := range i.Assignments[person] {
		if at.Before(assignment.From) || assignment.To != nil && !at.Before(*assignment.To) {
			continue
		}
		unit := i.ancestors(assignment.Unit)
		if unit.Group == nil || company.Valid && unit.Company != company || department.Valid && unit.Department != department {
			continue
		}
		if assignment.Kind == "PRIMARY" {
			primaryGroup.add(*unit.Group)
		} else {
			concurrentGroup.add(*unit.Group)
		}
	}
	if !primaryGroup.ID.Valid {
		primaryGroup = concurrentGroup
	}
	result := reportOrganization{Company: company, Department: department}
	if group := primaryGroup.value(); group.Valid {
		result.Group = &group
	}
	return result
}

func attachReportOrganizations(ctx context.Context, tx pgx.Tx, draft *paymentDraft, payments []paymentInput) error {
	if len(draft.Lines) == 0 && len(draft.Performance) == 0 && len(draft.Corrections) == 0 {
		return nil
	}
	peopleSet := make(map[int64]bool)
	for _, l := range draft.Lines {
		peopleSet[l.PersonID] = true
	}
	for _, p := range draft.Performance {
		if p.PersonID != 0 && p.PaymentVersionID == 0 {
			peopleSet[p.PersonID] = true
		}
	}
	for _, c := range draft.Corrections {
		peopleSet[c.PersonID] = true
	}
	people := make([]int64, 0, len(peopleSet))
	for person := range peopleSet {
		people = append(people, person)
	}
	index, err := loadReportAttribution(ctx, tx, people, payments)
	if err != nil {
		return err
	}
	byVersion := make(map[int64]sourceFact, len(payments))
	for _, payment := range payments {
		byVersion[payment.ID] = payment.Fact
	}
	for i := range draft.Lines {
		line := &draft.Lines[i]
		if line.BeneficiaryUnit != nil {
			line.organization = index.ancestors(*line.BeneficiaryUnit)
			continue
		}
		if line.reportUnit.Valid {
			line.organization = index.ancestors(line.reportUnit)
			continue
		}
		f := byVersion[line.FactID]
		var company identityKey
		if line.Component == "sales" {
			company = identityKey{f.Source, f.SalesCompanyID}
		}
		line.organization = index.resolve(line.PersonID, f.OccurredAt, company)
	}
	currentPaymentOrganizations := make(map[int64]reportOrganization)
	for i := range draft.Performance {
		post := &draft.Performance[i]
		if post.PaymentVersionID == 0 {
			f := byVersion[post.SourceVersionID]
			post.Organization = index.resolve(post.PersonID, f.OccurredAt, identityKey{f.Source, f.SalesCompanyID})
			currentPaymentOrganizations[post.SourceVersionID] = post.Organization
		} else if org, ok := currentPaymentOrganizations[post.PaymentVersionID]; ok {
			post.Organization = org
		}
	}
	if len(draft.Corrections) > 0 {
		if err := attributeCorrections(ctx, tx, draft.Corrections, index); err != nil {
			return err
		}
	}
	return nil
}

// Correct an existing recipient against their original booked organization.
// A recipient added by this change is resolved at the original payment time.
func attributeCorrectionRecipients(ctx context.Context, tx pgx.Tx, corrections []correctionLine) error {
	recipients := make([]int64, len(corrections))
	for i, line := range corrections {
		recipients[i] = line.PersonID
	}
	organizations, err := loadReportAttribution(ctx, tx, recipients, nil)
	if err != nil {
		return err
	}
	return attributeCorrections(ctx, tx, corrections, organizations)
}

func attributeCorrections(ctx context.Context, tx pgx.Tx, corrections []correctionLine, index reportAttribution) error {
	lineIDs, people := make([]int64, len(corrections)), make([]int64, len(corrections))
	units := make([]pgtype.UUID, len(corrections))
	for i, c := range corrections {
		lineIDs[i], people[i], units[i] = c.OriginalLineID, c.PersonID, c.Unit
	}
	rows, err := tx.Query(ctx, `SELECT q.n,p.occurred_at,origin.same,
	 previous.found,CASE WHEN origin.same THEN l.company_id ELSE previous.company_id END,
	 CASE WHEN origin.same THEN l.department_id ELSE previous.department_id END,
	 CASE WHEN origin.same THEN l.group_id ELSE previous.group_id END
	 FROM unnest($1::bigint[],$2::bigint[],$3::uuid[]) WITH ORDINALITY AS q(line_id,person_id,unit_id,n)
	 JOIN calculation_draft_lines l ON l.id=q.line_id
	 JOIN source_order_versions p ON p.id=l.source_version_id
	 CROSS JOIN LATERAL (
	  SELECT (nullif(q.person_id,0),q.unit_id) IS NOT DISTINCT FROM (l.person_id,l.beneficiary_unit_id) AS same
	 ) origin
	 LEFT JOIN LATERAL (
	  SELECT true AS found,c.company_id,c.department_id,c.group_id FROM calculation_draft_corrections c
	  JOIN settlements settled USING(period_id)
	  WHERE c.original_line_id=l.id AND (c.person_id,c.beneficiary_unit_id) IS NOT DISTINCT FROM (nullif(q.person_id,0),q.unit_id)
	   AND c.amount_minor>0
	  ORDER BY settled.confirmed_at,c.id LIMIT 1
	 ) previous ON true`, lineIDs, people, units)
	if err != nil {
		return err
	}
	var n int
	var at time.Time
	var original bool
	var prior *bool
	var org reportOrganization
	_, err = pgx.ForEachRow(rows, []any{&n, &at, &original, &prior, &org.Company, &org.Department, &org.Group}, func() error {
		c := &corrections[n-1]
		c.organization = org
		if !original && prior == nil && c.Unit.Valid {
			c.organization = index.ancestors(c.Unit)
		} else if !original && prior == nil {
			c.organization = index.resolve(c.PersonID, at, identityKey{})
		}
		return nil
	})
	return err
}
