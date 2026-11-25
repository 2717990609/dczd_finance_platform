package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var dispatchPhone = regexp.MustCompile(`^[0-9]{6,21}$`)
var dispatchCountry = regexp.MustCompile(`^\+[0-9]{1,4}$`)

type dispatchLookupInput struct{ Target, Phone, CountryCode string }
type dispatchAccount struct {
	Status    string `json:"status"`
	UserID    string `json:"user_id"`
	Name      string `json:"display_name"`
	State     string `json:"identity_state"`
	CompanyID string `json:"company_id"`
	MemberID  string `json:"member_id"`
	Role      string `json:"role"`
}

func (v dispatchLookupInput) normalized() (dispatchLookupInput, error) {
	if !validDispatchTarget(v.Target) {
		return v, invalid
	}
	v.Phone = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(v.Phone))
	v.CountryCode = strings.TrimSpace(v.CountryCode)
	if v.CountryCode == "" {
		v.CountryCode = "+86"
	} else if !strings.HasPrefix(v.CountryCode, "+") {
		v.CountryCode = "+" + v.CountryCode
	}
	if strings.HasPrefix(v.Phone, "+") {
		if !strings.HasPrefix(v.Phone, v.CountryCode) {
			return v, invalid
		}
		v.Phone = strings.TrimPrefix(v.Phone, v.CountryCode)
	}
	if !dispatchPhone.MatchString(v.Phone) || !dispatchCountry.MatchString(v.CountryCode) {
		return v, invalid
	}
	return v, nil
}
func (s workspace) lookupDispatchAccount(ctx context.Context, input dispatchLookupInput) (dispatchAccount, error) {
	var account dispatchAccount
	if _, err := s.appointmentOperator(ctx); err != nil {
		return account, err
	}
	input, err := input.normalized()
	if err != nil {
		return account, err
	}
	client, err := newOrganizationSyncClient(input.Target)
	if errors.Is(err, errSourceDisabled) {
		return account, dispatchProblem("该业务系统的组织同步通道未开启，暂时无法按手机号核验账号")
	}
	if err != nil {
		return account, err
	}
	if err := client.Post(ctx, organizationLookupPath, map[string]string{"phone": input.Phone, "country_code": input.CountryCode}, &account); err != nil {
		return dispatchAccount{}, errSourceRead
	}
	switch account.Status {
	case "FOUND":
		if !textOK(account.UserID, 128) || (account.State != "NONE" && (account.State != "ACTIVE" && account.State != "INACTIVE" || !textOK(account.MemberID, 128) || !textOK(account.CompanyID, 128) || (account.Role != "MEMBER" && account.Role != "OWNER" && account.Role != "MANAGER"))) {
			return dispatchAccount{}, errSourceRead
		}
	case "NOT_FOUND", "CONFLICT", "UNAVAILABLE":
		return dispatchAccount{Status: account.Status}, nil
	default:
		return dispatchAccount{}, errSourceRead
	}
	return account, nil
}

type dispatchBindInput struct {
	dispatchLookupInput
	PersonID          int64
	UserID, CompanyID string
	Owner             bool
}

func dispatchBindPhoneKey(input dispatchBindInput) ([]byte, error) {
	normalized, err := input.dispatchLookupInput.normalized()
	if err != nil || !textOK(input.UserID, 128) {
		return nil, invalid
	}
	key := os.Getenv("ORG_SYNC_PHONE_HASH_SECRET")
	if legacy := os.Getenv("FINANCE_ORG_SYNC_PHONE_HASH_SECRET"); legacy != "" {
		if key != "" && key != legacy {
			return nil, dispatchProblem("新旧手机号摘要密钥配置不一致")
		}
		key = legacy
	}
	if len(key) < 32 {
		return nil, dispatchProblem("账号绑定需要配置手机号摘要密钥")
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(normalized.CountryCode + normalized.Phone))
	return mac.Sum(nil), nil
}
func storeVerifiedDispatchBinding(ctx context.Context, tx pgx.Tx, by *string, input dispatchBindInput, account dispatchAccount, phoneKey []byte, effective *time.Time) (string, error) {
	var binding string
	var name string
	if err := tx.QueryRow(ctx, "SELECT name FROM people WHERE id=$1 FOR UPDATE", input.PersonID).Scan(&name); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", input.Target+":"+account.UserID); err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_phone_bindings VALUES($1,$2) ON CONFLICT(phone_key) DO UPDATE SET person_id=excluded.person_id WHERE organization_dispatch_phone_bindings.person_id=excluded.person_id`, phoneKey, input.PersonID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", dispatchProblem("同一手机号已绑定其他财务人员")
	}
	var owner int64
	err = tx.QueryRow(ctx, "SELECT entity_id::text,person_id FROM organization_dispatch_links WHERE target=$1 AND entity_type='MEMBER' AND external_id=$2", input.Target, account.UserID).Scan(&binding, &owner)
	if err == nil {
		if owner != input.PersonID {
			return "", conflict
		}
		return binding, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	from := time.Now().UTC()
	if effective != nil {
		from = effective.UTC()
	}
	var mappingExists, clashes bool
	err = tx.QueryRow(ctx, `WITH candidates AS (SELECT person_id,valid_from,valid_to FROM identity_mappings WHERE source=$1 AND external_id=$2
  UNION ALL SELECT i.person_id,i.valid_from,i.valid_to FROM catalog_sales_people_sources i JOIN catalog_sales_context c ON c.source=i.source AND c.captured_at=i.observed_at WHERE i.source=$1 AND i.external_id=$2)
  SELECT EXISTS(SELECT 1 FROM identity_mappings WHERE source=$1 AND external_id=$2 AND person_id=$3 AND tstzrange(valid_from,valid_to,'[)') @> $4::timestamptz),
  EXISTS(SELECT 1 FROM candidates WHERE (valid_to IS NULL OR valid_to>$4) AND (person_id!=$3 OR valid_from>$4))`, input.Target, account.UserID, input.PersonID, from).Scan(&mappingExists, &clashes)
	if err != nil {
		return "", err
	}
	if clashes {
		return "", dispatchProblem("来源账号已有其他归属或未来生效记录")
	}
	if !mappingExists {
		if _, err := tx.Exec(ctx, `INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES($1,$2,$3,$4)`, input.PersonID, input.Target, account.UserID, from); err != nil {
			return "", err
		}
		if err := enqueueDraftConfiguration(ctx, tx, draftRefreshIdentity, input.Target, from); err != nil {
			return "", err
		}
	}
	binding = uuid.NewString()
	state, role, company := account.State, account.Role, account.CompanyID
	var payload map[string]any
	if state == "NONE" || input.Owner {
		wire, external, err := readyDispatchCompany(ctx, tx, input.CompanyID, input.Target)
		if err != nil {
			return "", err
		}
		if state == "NONE" {
			if !textOK(name, 25) {
				return "", errDispatchPersonName
			}
			state, role, company = "PENDING", "MEMBER", external
			payload = map[string]any{"operation": "CREATE", "company_id": wire, "expected_company_external_id": external, "external_user_id": account.UserID, "name": name}
			if input.Owner {
				payload["desired_role"] = "OWNER"
			}
		} else if state != "ACTIVE" || company != external {
			return "", dispatchProblem("该账号当前销售身份与所选公司不符")
		} else if role != "OWNER" {
			payload = map[string]any{"operation": "SET_OWNER", "external_user_id": account.UserID, "expected_company_external_id": company, "external_member_id": account.MemberID}
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id,person_id,external_member_id,external_company_id,identity_state,role,source_name)
  VALUES($1,'MEMBER',$2,$3,$4,$5,$6,$7,$8,$9)`, input.Target, binding, account.UserID, input.PersonID, account.MemberID, company, state, role, account.Name)
	if err != nil {
		return "", err
	}
	if payload != nil {
		_, err = enqueueDispatch(ctx, tx, input.Target, "MEMBER", binding, "sale_company_membership_set", payload, effective, by)
	}
	return binding, err
}
func (s workspace) bindDispatchAccount(ctx context.Context, input dispatchBindInput) (map[string]string, error) {
	if input.PersonID < 1 {
		return nil, invalid
	}
	key, err := dispatchBindPhoneKey(input)
	if err != nil {
		return nil, err
	}
	account, err := s.lookupDispatchAccount(ctx, input.dispatchLookupInput)
	if err != nil {
		return nil, err
	}
	if account.Status != "FOUND" || account.UserID != input.UserID {
		return nil, dispatchProblem("来源账号已变化，请重新查询")
	}
	var binding string
	err = s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		var err error
		binding, err = storeVerifiedDispatchBinding(ctx, tx, by, input, account, key, nil)
		return err
	})
	return map[string]string{"bindingId": binding}, err
}

const readyDispatchCompanyFrom = ` FROM organization_dispatch_company_keys k JOIN organization_units u ON u.id=k.unit_id AND u.status='ACTIVE'
 JOIN organization_dispatch_targets t ON t.company_id=u.id AND t.target=$1
 JOIN organization_dispatch_links l ON l.target=t.target AND l.entity_type='COMPANY' AND l.entity_id=u.id
 WHERE NOT EXISTS(SELECT 1 FROM organization_dispatch_outbox q WHERE q.target=t.target AND q.entity_type='COMPANY'
 AND q.entity_id=u.id AND q.version>l.version AND q.state!='STOPPED')`

func readyDispatchCompany(ctx context.Context, tx pgx.Tx, id, target string) (string, string, error) {
	id, ok := canonicalSourceUUID(id)
	if !ok {
		return "", "", invalid
	}
	var wire, external string
	err := tx.QueryRow(ctx, `SELECT k.wire_id::text,l.external_id`+readyDispatchCompanyFrom+` AND u.id=$2 FOR SHARE OF t,u,l`, target, id).Scan(&wire, &external)
	if errors.Is(err, pgx.ErrNoRows) {
		err = dispatchProblem("请先完成该公司在所选系统的同步")
	}
	return wire, external, err
}

type dispatchMemberInput struct {
	RequestID                           string
	PersonID                            int64
	BindingID, Operation, DestinationID string
	Reason                              string `json:",omitempty"`
	ExpectedVersion                     int64
	EffectiveAt                         *time.Time
}

func (s workspace) operateDispatchMember(ctx context.Context, input dispatchMemberInput) (map[string]string, error) {
	id, ok := canonicalSourceUUID(input.BindingID)
	if !ok || input.PersonID < 1 || input.ExpectedVersion < 0 || len([]rune(input.Reason)) > 256 {
		return nil, invalid
	}
	if input.RequestID != "" {
		if _, ok := canonicalSourceUUID(input.RequestID); !ok {
			return nil, invalid
		}
	}
	var result string
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		var target, userID, member, company, role, state, name string
		var version int64
		err := tx.QueryRow(ctx, `SELECT l.target,l.external_id,l.external_member_id,l.external_company_id,l.role,l.identity_state,p.name,l.version
   FROM organization_dispatch_links l JOIN people p ON p.id=l.person_id WHERE entity_type='MEMBER' AND entity_id=$1 AND person_id=$2`, id, input.PersonID).Scan(&target, &userID, &member, &company, &role, &state, &name, &version)
		if err != nil {
			return err
		}
		if err := lockDispatchEntity(ctx, tx, target, "MEMBER", id); err != nil {
			return err
		}
		var current int64
		if err := tx.QueryRow(ctx, `SELECT version FROM organization_dispatch_links WHERE target=$1 AND entity_id=$2 AND entity_type='MEMBER' FOR UPDATE`, target, id).Scan(&current); err != nil {
			return err
		}
		var command []byte
		if input.RequestID != "" {
			command, _ = json.Marshal(input)
			var same bool
			err := tx.QueryRow(ctx, `SELECT message_id::text,actor IS NOT DISTINCT FROM $2::uuid AND input=$3::jsonb FROM organization_dispatch_commands WHERE request_id=$1`, input.RequestID, by, command).Scan(&result, &same)
			if err == nil {
				if !same {
					return conflict
				}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if version != current || current != input.ExpectedVersion {
			return conflict
		}
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_dispatch_outbox WHERE target=$1 AND entity_type='MEMBER' AND entity_id=$2 AND state NOT IN ('APPLIED','STOPPED','CONFLICT'))`, target, id).Scan(&busy); err != nil {
			return err
		}
		if busy || member == "" {
			return dispatchProblem("请先处理该账号已有的身份消息")
		}
		p := map[string]any{"operation": input.Operation, "external_user_id": userID, "external_member_id": member, "expected_company_external_id": company}
		switch input.Operation {
		case "ENABLE":
			if state != "INACTIVE" {
				return conflict
			}
		case "DISABLE":
			if role == "OWNER" || state != "ACTIVE" {
				return dispatchProblem("仅可停用启用中的非负责人身份")
			}
		case "SET_OWNER":
			if state != "ACTIVE" || role == "OWNER" {
				return conflict
			}
		case "UNSET_OWNER":
			if state != "ACTIVE" || role != "OWNER" {
				return conflict
			}
		case "TRANSFER":
			if state != "ACTIVE" || role == "OWNER" {
				return dispatchProblem("调动前须移交负责人并启用身份")
			}
			wire, external, err := readyDispatchCompany(ctx, tx, input.DestinationID, target)
			if err != nil {
				return err
			}
			if external == company {
				return invalid
			}
			p["destination_company_id"], p["destination_company_external_id"] = wire, external
		case "RENAME":
			if !textOK(name, 25) {
				return errDispatchPersonName
			}
			if state != "ACTIVE" {
				return conflict
			}
			p["name"] = name
		default:
			return invalid
		}
		result, err = enqueueDispatch(ctx, tx, target, "MEMBER", id, "sale_company_membership_set", p, input.EffectiveAt, by)
		if err == nil && input.RequestID != "" {
			_, err = tx.Exec(ctx, `INSERT INTO organization_dispatch_commands(request_id,message_id,input,actor) VALUES($1,$2,$3,$4)`, input.RequestID, result, command, by)
		}
		return err
	})
	return map[string]string{"messageId": result}, err
}
