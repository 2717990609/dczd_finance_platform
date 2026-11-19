package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	financeAccountCookie = "finance_account_session"
	financeSessionTTL    = 8 * time.Hour
)

var (
	errFinanceLogin     = errors.New("请登录财务工作台")
	errFinancePassword  = errors.New("账号或密码有误，请核对后重试")
	errFinanceForbidden = errors.New("当前账号没有此操作权限")
	errFinanceLoginBusy = errors.New("登录请求较多，请稍后重试")
	financeLoginSlots   = make(chan struct{}, 2)

	// Use a valid hash for unknown accounts to keep password-check timing comparable.
	financeUnknownPassword, _ = bcrypt.GenerateFromPassword([]byte("unusable-password-hash"), bcrypt.DefaultCost)
)

type financePrincipal struct {
	ID                 string             `json:"-"`
	Name               string             `json:"name"`
	Workspace          bool               `json:"workspace"`
	Rules              []financeRuleGrant `json:"rules"`
	SettlementProducts []int64            `json:"settlementProducts"`
	sessionHash        []byte
}

type financeSessionView struct {
	Development bool              `json:"development"`
	User        *financePrincipal `json:"user"`
}

type financeLoginInput struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type financePrincipalKey struct{}

func financeUser(ctx context.Context) *financePrincipal {
	p, _ := ctx.Value(financePrincipalKey{}).(*financePrincipal)
	return p
}

func (p *financePrincipal) permitsRule(product int64, publish bool) bool {
	if p != nil {
		for _, grant := range p.Rules {
			if grant.ProductID == product && (!publish || grant.Publish) {
				return true
			}
		}
	}
	return false
}

func financeSessionKey(r *http.Request) []byte {
	cookie, err := r.Cookie(financeAccountCookie)
	if err != nil {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != 32 {
		return nil
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}

func (s workspace) accountSession(r *http.Request) (*financePrincipal, error) {
	hash := financeSessionKey(r)
	if hash == nil {
		return nil, nil
	}
	var p financePrincipal
	err := s.db.QueryRow(r.Context(), `SELECT a.id::text,p.name,a.workspace_access,
	 coalesce((SELECT jsonb_agg(jsonb_build_object('productId',g.product_id,'publish',g.can_publish) ORDER BY g.product_id)
	  FROM finance_account_rule_grants g WHERE g.account_id=a.id),'[]'),
	 ARRAY(SELECT g.product_id FROM finance_account_settlement_grants g WHERE g.account_id=a.id)
	 FROM finance_account_sessions s JOIN finance_accounts a ON a.id=s.account_id JOIN people p ON p.id=a.person_id
	 WHERE s.token_hash=$1 AND s.expires_at>now() AND a.enabled`, hash).Scan(&p.ID, &p.Name, &p.Workspace, &p.Rules, &p.SettlementProducts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.sessionHash = hash
	return &p, nil
}

func (s workspace) loginFinanceAccount(ctx context.Context, input financeLoginInput) (string, error) {
	login := normalizeFinanceLogin(input.Login)
	if !financeLoginPattern.MatchString(login) || len(input.Password) == 0 || len(input.Password) > 72 {
		return "", errFinancePassword
	}
	select {
	case financeLoginSlots <- struct{}{}:
		defer func() { <-financeLoginSlots }()
	default:
		return "", errFinanceLoginBusy
	}

	var token string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var id, hash string
		var enabled, locked bool
		err := tx.QueryRow(ctx, `SELECT id::text,password_hash,enabled,coalesce(retry_after>now(),false)
		 FROM finance_accounts WHERE login=$1 FOR UPDATE`, login).Scan(&id, &hash, &enabled, &locked)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = bcrypt.CompareHashAndPassword(financeUnknownPassword, []byte(input.Password))
			return nil
		}
		if err != nil {
			return err
		}
		ok := bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) == nil
		if !enabled || locked {
			return nil
		}
		if !ok {
			_, err = tx.Exec(ctx, `UPDATE finance_accounts SET
			 failures=CASE WHEN failures>=4 THEN 0 ELSE failures+1 END,
			 retry_after=CASE WHEN failures>=4 THEN now()+interval '5 minutes' ELSE NULL END
			 WHERE id=$1::uuid`, id)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE finance_accounts SET failures=0,retry_after=NULL WHERE id=$1::uuid`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM finance_account_sessions WHERE account_id=$1::uuid AND expires_at<=now()`, id); err != nil {
			return err
		}
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return err
		}
		digest := sha256.Sum256(secret[:])
		if _, err := tx.Exec(ctx, `INSERT INTO finance_account_sessions(token_hash,account_id,expires_at)
		 VALUES($1,$2::uuid,now()+$3::interval)`, digest[:], id, financeSessionTTL); err != nil {
			return err
		}
		token = base64.RawURLEncoding.EncodeToString(secret[:])
		return nil
	})
	if err == nil && token == "" {
		err = errFinancePassword
	}
	return token, err
}

func financeResponseCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	// The local development URL can use plain HTTP. All other hosts require HTTPS.
	origin, _ := url.Parse(r.Header.Get("Origin"))
	localHTTP := origin != nil && origin.Scheme == "http" && isLoopbackHost(origin.Hostname()) && r.TLS == nil
	return &http.Cookie{Name: financeAccountCookie, Value: value, Path: "/api/v1", MaxAge: maxAge,
		HttpOnly: true, Secure: !localHTTP, SameSite: http.SameSiteLaxMode}
}

func (s workspace) getFinanceSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, err := s.accountSession(r)
	respond(w, financeSessionView{Development: s.development, User: user}, err)
}

func (s workspace) postFinanceSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !catalogCookieWriteAllowed(r) {
		respond(w, nil, errFinanceForbidden)
		return
	}
	origin, _ := url.Parse(r.Header.Get("Origin"))
	if r.TLS == nil && (origin == nil || (origin.Scheme != "https" && !isLoopbackHost(origin.Hostname()))) {
		respond(w, nil, errFinanceForbidden)
		return
	}
	submit(func(ctx context.Context, input financeLoginInput) (any, error) {
		token, err := s.loginFinanceAccount(ctx, input)
		if err != nil {
			return nil, err
		}
		http.SetCookie(w, financeResponseCookie(r, token, int(financeSessionTTL.Seconds())))
		return map[string]bool{"ok": true}, nil
	})(w, r)
}

func (s workspace) deleteFinanceSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !catalogCookieWriteAllowed(r) {
		respond(w, nil, errFinanceForbidden)
		return
	}
	if key := financeSessionKey(r); key != nil {
		if _, err := s.db.Exec(r.Context(), `DELETE FROM finance_account_sessions WHERE token_hash=$1`, key); err != nil {
			respond(w, nil, err)
			return
		}
	}
	http.SetCookie(w, financeResponseCookie(r, "", -1))
	respond(w, map[string]bool{"ok": true}, nil)
}
