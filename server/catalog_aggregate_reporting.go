package main

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type catalogAggregateReportPayment struct {
	id        int64
	paidAt    time.Time
	principal int64
	claim     vipClaim
	company   pgtype.UUID
	booked    bool
	posting   draftPerformance
}

// The accepted events already contain the version of the original payment
// used by the calculation, including the frozen version for a later refund.
const catalogAggregateReportPaymentsSQL = `
 SELECT p.id,p.occurred_at,(p.fact->>'amountMinor')::bigint,
  coalesce(c.sales_claims,'[]'),coalesce(company.company_id,''),
  original.source_version_id IS NOT NULL,coalesce(original.person_id,0),
  original.company_id,original.department_id,original.group_id
 FROM source_order_versions p
 LEFT JOIN source_order_classifications c ON c.source_version_id=p.id
 LEFT JOIN source_payment_companies company ON company.source_version_id=p.id
 LEFT JOIN LATERAL (
  SELECT a.source_version_id,a.person_id,a.company_id,a.department_id,a.group_id
  FROM calculation_draft_performance a
  JOIN settlements final USING(period_id)
  JOIN periods prior ON prior.id=a.period_id
  WHERE a.source_version_id=p.id AND a.payment_version_id IS NULL AND prior.aggregate_package_id=$1::uuid
  ORDER BY final.id LIMIT 1
 ) original ON true
 WHERE p.id=ANY($2::bigint[])`

func loadCatalogAggregateReporting(ctx context.Context, tx pgx.Tx, packageID string,
	events []catalogAggregateEventReference, groups []catalogPeriodGroup) (reportAttribution, map[int64]catalogAggregateReportPayment, error) {
	paymentIDs := make(map[int64]bool)
	for _, event := range events {
		paymentIDs[event.PaymentID] = true
	}
	ids := make([]int64, 0, len(paymentIDs))
	for id := range paymentIDs {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, catalogAggregateReportPaymentsSQL, packageID, ids)
	if err != nil {
		return reportAttribution{}, nil, err
	}
	payments := make(map[int64]catalogAggregateReportPayment, len(ids))
	var users, companies []string
	var payment catalogAggregateReportPayment
	var claims []vipClaim
	var sourceCompany string
	org := &payment.posting.Organization
	_, err = pgx.ForEachRow(rows, []any{&payment.id, &payment.paidAt, &payment.principal, &claims, &sourceCompany,
		&payment.booked, &payment.posting.PersonID, &org.Company, &org.Department, &org.Group}, func() error {
		payment.claim, _ = vipSellerClaim(claims)
		if payment.claim.Company == "" {
			payment.claim.Company = sourceCompany
		}
		if !payment.booked {
			users = append(users, payment.claim.User)
			companies = append(companies, payment.claim.Company)
		}
		payments[payment.id] = payment
		return nil
	})
	if err != nil {
		return reportAttribution{}, nil, err
	}

	rows, err = tx.Query(ctx, catalogPerformanceLinksSQL, users, companies)
	if err != nil {
		return reportAttribution{}, nil, err
	}
	sellerLinks := [2]map[string][]vipLink{make(map[string][]vipLink), make(map[string][]vipLink)}
	companyLinks := [2]map[string][]vipLink{make(map[string][]vipLink), make(map[string][]vipLink)}
	var kind string
	var level int
	var link vipLink
	_, err = pgx.ForEachRow(rows, []any{&kind, &level, &link.Key, &link.Value, &link.From, &link.To}, func() error {
		if kind == "PERSON" {
			sellerLinks[level][link.Key] = append(sellerLinks[level][link.Key], link)
		} else {
			companyLinks[level][link.Key] = append(companyLinks[level][link.Key], link)
		}
		return nil
	})
	if err != nil {
		return reportAttribution{}, nil, err
	}

	recipients := make(map[int64]bool)
	for _, group := range groups {
		for _, target := range group.posting.Targets {
			if target.Person != 0 {
				recipients[target.Person] = true
			}
		}
	}
	for id, payment := range payments {
		if payment.booked {
			continue
		}
		seller, _ := catalogPerformanceLinkAt(sellerLinks, payment.claim.User, payment.paidAt)
		company, _ := catalogPerformanceLinkAt(companyLinks, payment.claim.Company, payment.paidAt)
		payment.posting.PersonID, _ = strconv.ParseInt(seller, 10, 64)
		if company != "" {
			if err := payment.company.Scan(company); err != nil {
				return reportAttribution{}, nil, err
			}
		}
		if payment.posting.PersonID != 0 {
			recipients[payment.posting.PersonID] = true
		}
		payments[id] = payment
	}
	people := make([]int64, 0, len(recipients))
	for id := range recipients {
		people = append(people, id)
	}
	organizations, err := loadReportAttribution(ctx, tx, people, nil)
	if err != nil {
		return reportAttribution{}, nil, err
	}
	for id, payment := range payments {
		if !payment.booked {
			payment.posting.Organization = organizations.resolveAssignments(payment.posting.PersonID, payment.paidAt,
				payment.company, payment.claim.Company == "")
			payments[id] = payment
		}
	}
	return organizations, payments, nil
}
