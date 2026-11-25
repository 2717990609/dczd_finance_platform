package main

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strings"
)

type dispatchRenameInput struct {
	PersonID           int64
	Name, ExpectedName string
}

func (s workspace) renameDispatchPerson(ctx context.Context, input dispatchRenameInput) (map[string]string, error) {
	name := strings.TrimSpace(input.Name)
	if !textOK(name, 25) {
		return nil, errDispatchPersonName
	}
	if input.PersonID < 1 || input.ExpectedName == "" {
		return nil, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		var current string
		if err := tx.QueryRow(ctx, "SELECT name FROM people WHERE id=$1 FOR UPDATE", input.PersonID).Scan(&current); err != nil {
			return err
		}
		if current == name {
			return nil
		}
		if current != input.ExpectedName {
			return conflict
		}
		rows, err := tx.Query(ctx, `SELECT target,entity_id::text,external_id,external_company_id,external_member_id FROM organization_dispatch_links
   WHERE entity_type='MEMBER' AND person_id=$1 AND identity_state IN ('ACTIVE','PENDING') ORDER BY target,entity_id`, input.PersonID)
		if err != nil {
			return err
		}
		type binding struct{ Target, ID, User, Company, Member string }
		list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[binding])
		if err != nil {
			return err
		}
		for _, b := range list {
			if err := lockDispatchEntity(ctx, tx, b.Target, "MEMBER", b.ID); err != nil {
				return err
			}
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM organization_dispatch_outbox WHERE target=$1 AND entity_type='MEMBER' AND entity_id=$2
    AND state NOT IN ('STOPPED','APPLIED') AND version=(SELECT max(version) FROM organization_dispatch_outbox WHERE target=$1 AND entity_type='MEMBER' AND entity_id=$2))`, b.Target, b.ID).Scan(&busy); err != nil {
				return err
			}
			if busy || b.Member == "" {
				return dispatchProblem("请先完成该人员的在途身份变更")
			}
			_, err := enqueueDispatch(ctx, tx, b.Target, "MEMBER", b.ID, "sale_company_membership_set", map[string]any{"operation": "RENAME", "name": name, "external_user_id": b.User, "external_member_id": b.Member, "expected_company_external_id": b.Company}, nil, by)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE people SET name=$2,dispatch_name_managed=true WHERE id=$1", input.PersonID, name)
		return err
	})
	return map[string]string{"name": name}, err
}
