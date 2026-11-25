package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	"math"
)

type dispatchSnapshotPhone struct{ PhoneKey, PersonID string }

type dispatchSnapshotCompany struct {
	UnitID, EntityID, InternalUnitID, ParentEntityID string
	Targets                                          []string
}
type dispatchSnapshotReceipt struct {
	Target, Kind, EntityID, ExternalID string
	Version                            int64
}
type dispatchSnapshotBinding struct {
	EntityID, PersonID, Target, UserID, MemberID, CompanyID, Role, State string
	Version                                                              int64
}

func importDispatchSnapshot(ctx context.Context, tx pgx.Tx, s organizationSnapshot) error {
	if s.DispatchVersionFloor == nil {
		if len(s.DispatchPhoneBindings)+len(s.DispatchCompanies)+len(s.DispatchReceipts)+len(s.DispatchBindings) > 0 {
			return invalid
		}
		return nil
	}
	floor := *s.DispatchVersionFloor
	if floor < 0 || floor == math.MaxInt64 {
		return invalid
	}
	for _, c := range s.DispatchCompanies {
		tag, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_company_keys(unit_id,wire_id,parent_wire_id)
   SELECT id,$2,nullif($3,'')::uuid FROM organization_units WHERE id=$1 AND sales_company_type IS NOT NULL ON CONFLICT(unit_id) DO UPDATE
   SET parent_wire_id=CASE WHEN (SELECT dispatch_profile_version FROM organization_units WHERE id=excluded.unit_id)=0 THEN excluded.parent_wire_id ELSE organization_dispatch_company_keys.parent_wire_id END WHERE organization_dispatch_company_keys.wire_id=excluded.wire_id`, c.UnitID, c.EntityID, c.ParentEntityID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return conflict
		}
		if c.InternalUnitID != "" {
			tag, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_units(unit_id,company_id) VALUES($1,$2)
             ON CONFLICT(unit_id) DO UPDATE SET company_id=organization_dispatch_units.company_id WHERE organization_dispatch_units.company_id=excluded.company_id`, c.InternalUnitID, c.UnitID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return conflict
			}
		}
		var managed bool
		if err := tx.QueryRow(ctx, "SELECT targets_managed FROM organization_dispatch_company_keys WHERE unit_id=$1", c.UnitID).Scan(&managed); err != nil {
			return err
		}
		if !managed {
			if _, err := tx.Exec(ctx, "DELETE FROM organization_dispatch_targets WHERE company_id=$1", c.UnitID); err != nil {
				return err
			}
		}
		for _, target := range c.Targets {
			if !validDispatchTarget(target) {
				return invalid
			}
			if !managed {
				if _, err := tx.Exec(ctx, "INSERT INTO organization_dispatch_targets VALUES($1,$2) ON CONFLICT DO NOTHING", c.UnitID, target); err != nil {
					return err
				}
			}
		}
	}
	for _, r := range s.DispatchReceipts {
		if !validDispatchTarget(r.Target) || r.Version < 1 || r.Version > floor || !textOK(r.ExternalID, 128) {
			return invalid
		}
		id := r.EntityID
		switch r.Kind {
		case "COMPANY":
			if err := tx.QueryRow(ctx, "SELECT unit_id::text FROM organization_dispatch_company_keys WHERE wire_id=$1", r.EntityID).Scan(&id); err != nil {
				return err
			}
		case "CATEGORY":
			if err := tx.QueryRow(ctx, "SELECT id::text FROM organization_sales_categories WHERE id=$1", r.EntityID).Scan(&id); err != nil {
				return err
			}
		default:
			return invalid
		}
		tag, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id,version) VALUES($1,$2,$3,$4,$5)
   ON CONFLICT(target,entity_type,entity_id) DO UPDATE SET version=greatest(organization_dispatch_links.version,excluded.version)
   WHERE organization_dispatch_links.external_id=excluded.external_id`, r.Target, r.Kind, id, r.ExternalID, r.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return conflict
		}
	}
	for _, b := range s.DispatchBindings {
		if !validDispatchTarget(b.Target) || !textOK(b.UserID, 128) || !textOK(b.CompanyID, 128) || !textOK(b.MemberID, 128) || b.State != "ACTIVE" && b.State != "INACTIVE" || b.Role != "OWNER" && b.Role != "MEMBER" && b.Role != "MANAGER" || b.Version < 0 || b.Version > floor {
			return invalid
		}
		tag, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id,person_id,external_company_id,external_member_id,role,identity_state,version)
  SELECT $1,'MEMBER',$2,$3,p.id,$5,$6,$7,$8,$9 FROM people p WHERE p.legacy_party_id=$4
  ON CONFLICT(target,entity_type,entity_id) DO UPDATE SET version=organization_dispatch_links.version
  WHERE organization_dispatch_links.person_id=excluded.person_id AND organization_dispatch_links.external_id=excluded.external_id
  AND (organization_dispatch_links.version>excluded.version OR (organization_dispatch_links.version,organization_dispatch_links.external_company_id,organization_dispatch_links.external_member_id,organization_dispatch_links.role,organization_dispatch_links.identity_state)=(excluded.version,excluded.external_company_id,excluded.external_member_id,excluded.role,excluded.identity_state))`, b.Target, b.EntityID, b.UserID, b.PersonID, b.CompanyID, b.MemberID, b.Role, b.State, b.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return conflict
		}
	}
	for _, phone := range s.DispatchPhoneBindings {
		digest, err := hex.DecodeString(phone.PhoneKey)
		if err != nil || len(digest) != 32 {
			return invalid
		}
		tag, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_phone_bindings(phone_key,person_id)
          SELECT $1,id FROM people WHERE legacy_party_id=$2 ON CONFLICT(phone_key) DO UPDATE SET person_id=excluded.person_id
          WHERE organization_dispatch_phone_bindings.person_id=excluded.person_id`, digest, phone.PersonID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return dispatchProblem("手机号摘要对应的人员归属与已有记录不同")
		}
	}
	if floor > 0 {
		// Lock allocation before reading the watermark. RESTART participates in the snapshot transaction.
		if _, err := tx.Exec(ctx, "ALTER SEQUENCE organization_dispatch_version INCREMENT BY 1"); err != nil {
			return err
		}
		var n int64
		var used bool
		if err := tx.QueryRow(ctx, "SELECT last_value,is_called FROM organization_dispatch_version").Scan(&n, &used); err != nil {
			return err
		}
		if n < floor || n == floor && !used {
			_, err := tx.Exec(ctx, fmt.Sprintf("ALTER SEQUENCE organization_dispatch_version RESTART WITH %d", floor+1))
			return err
		}
	}
	return nil
}
