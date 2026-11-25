package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

type intakeBinding struct {
	dispatchBindInput
	Confirmed dispatchAccount
}
type personIntakeInput struct {
	MembershipCompanyID string
	MemberRole          string
	StartsAt            *time.Time
	RequestID           string
	PersonID            int64
	Name                string
	Bindings            []intakeBinding
}
type verifiedIntakeBinding struct {
	Target, CompanyID string
	Owner             bool
	PhoneKey          []byte
	Confirmed         dispatchAccount
}

func (s workspace) savePersonIntake(ctx context.Context, in personIntakeInput) (map[string]int64, error) {
	actor, err := s.appointmentOperator(ctx)
	if err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	request, ok := canonicalSourceUUID(in.RequestID)
	if !ok || in.PersonID < 0 || len(in.Bindings) > 32 || in.PersonID == 0 && !textOK(in.Name, 25) || in.PersonID > 0 && (in.Name != "" || len(in.Bindings) == 0 && in.MembershipCompanyID == "") {
		return nil, invalid
	}
	if in.MembershipCompanyID != "" {
		if _, ok := canonicalSourceUUID(in.MembershipCompanyID); !ok || (in.MemberRole != "OWNER" && in.MemberRole != "MANAGER" && in.MemberRole != "MEMBER") {
			return nil, invalid
		}
	} else if in.MemberRole != "" || in.StartsAt != nil {
		return nil, invalid
	}
	// Use one lock order even when two users enter the same accounts in a different order.
	sort.Slice(in.Bindings, func(i, j int) bool {
		a, b := in.Bindings[i], in.Bindings[j]
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.UserID < b.UserID
	})
	verified := make([]verifiedIntakeBinding, len(in.Bindings))
	ownerTarget := map[string]bool{}
	for i, b := range in.Bindings {
		if in.MembershipCompanyID != "" && (b.Owner || b.Confirmed.State == "NONE") && b.CompanyID != in.MembershipCompanyID {
			return nil, dispatchProblem("绑定所用销售公司须与本次登记公司一致")
		}
		if b.Owner && ownerTarget[b.Target] {
			return nil, dispatchProblem("每个目标系统只能选一个负责人账号")
		}
		if b.Owner {
			ownerTarget[b.Target] = true
		}
		if b.PersonID != 0 || b.Confirmed.Status != "FOUND" || b.UserID != b.Confirmed.UserID || i > 0 && b.Target == in.Bindings[i-1].Target && b.UserID == in.Bindings[i-1].UserID {
			return nil, invalid
		}
		key, err := dispatchBindPhoneKey(b.dispatchBindInput)
		if err != nil {
			return nil, err
		}
		verified[i] = verifiedIntakeBinding{b.Target, b.CompanyID, b.Owner, key, b.Confirmed}
	}
	raw, err := json.Marshal(struct {
		Name          string
		PersonID      int64
		Company, Role string
		StartsAt      *time.Time
		Accounts      []verifiedIntakeBinding
	}{in.Name, in.PersonID, in.MembershipCompanyID, in.MemberRole, in.StartsAt, verified})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	var by *string
	if actor != nil {
		by = &actor.ID
	}
	const priorSQL = `SELECT person_id,actor IS NOT DISTINCT FROM $2::uuid AND fingerprint=$3 FROM organization_person_intakes WHERE request_id=$1`
	prior := func(row pgx.Row) (int64, error) {
		var person int64
		var same bool
		err := row.Scan(&person, &same)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		if err == nil && !same {
			return 0, conflict
		}
		return person, err
	}
	id, err := prior(s.db.QueryRow(ctx, priorSQL, request, by, hash[:]))
	if err != nil || id != 0 {
		return map[string]int64{"personId": id}, err
	}
	lookups, cancelCtx := errgroup.WithContext(ctx)
	lookups.SetLimit(4)
	for _, binding := range in.Bindings {
		lookups.Go(func() error {
			account, err := s.lookupDispatchAccount(cancelCtx, binding.dispatchLookupInput)
			if err != nil {
				return err
			}
			if account != binding.Confirmed {
				return dispatchProblem("来源账号或销售身份已变化，请重新查询并确认")
			}
			return nil
		})
	}
	if err = lookups.Wait(); err != nil {
		return nil, err
	}
	err = s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "person-intake:"+request); err != nil {
			return err
		}
		var err error
		id, err = prior(tx.QueryRow(ctx, priorSQL, request, by, hash[:]))
		if err != nil || id != 0 {
			return err
		}
		id = in.PersonID
		if id == 0 {
			if err := tx.QueryRow(ctx, "INSERT INTO people(name) VALUES($1) RETURNING id", in.Name).Scan(&id); err != nil {
				return err
			}
		}
		if in.MembershipCompanyID != "" {
			if err := lockAppointmentPerson(ctx, tx, id); err != nil {
				return err
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT status='ACTIVE' AND sales_company_type IS NOT NULL FROM organization_units WHERE id=$1 FOR UPDATE`, in.MembershipCompanyID).Scan(&active); err != nil {
				return err
			}
			if !active {
				return errAppointmentUnitInactive
			}
			from := time.Now().UTC()
			if in.StartsAt != nil {
				from = in.StartsAt.UTC()
			}
			if _, err := insertAppointment(ctx, tx, id, appointmentTarget{UnitID: in.MembershipCompanyID, Kind: in.MemberRole}, from, nil, by); err != nil {
				return err
			}
			if err := enqueueDraftConfiguration(ctx, tx, draftRefreshAppointment, from, nil, id); err != nil {
				return err
			}
		}
		for i, b := range in.Bindings {
			b.PersonID = id
			if _, err := storeVerifiedDispatchBinding(ctx, tx, by, b.dispatchBindInput, b.Confirmed, verified[i].PhoneKey, in.StartsAt); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO organization_person_intakes(request_id,fingerprint,person_id,actor) VALUES($1,$2,$3,$4)`, request, hash[:], id, by)
		return err
	})
	return map[string]int64{"personId": id}, err
}
