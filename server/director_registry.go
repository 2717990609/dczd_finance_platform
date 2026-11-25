package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/2717990609/dczd_finance_platform/internal/director"
	"github.com/jackc/pgx/v5"
)

const directorUploadLimit = 16 << 20 // Base64 workbook (10 MiB) and row bindings.

type directorEdit struct {
	Kind       string            `json:"kind"`
	FileName   string            `json:"fileName,omitempty"`
	File       []byte            `json:"file,omitempty"`
	Bindings   []directorBinding `json:"bindings,omitempty"`
	Department string            `json:"department,omitempty"`
	Company    string            `json:"companyId,omitempty"`
	Person     string            `json:"personId,omitempty"`
	Director   string            `json:"directorCompanyId,omitempty"`
}
type directorRequest struct {
	PeriodID  int64        `json:"periodId"`
	BaseID    int64        `json:"baseId"`
	Edit      directorEdit `json:"edit"`
	Expected  string       `json:"expected,omitempty"`
	RequestID string       `json:"requestId,omitempty"`
}
type directorPreview struct {
	Period   directorPeriod    `json:"period"`
	BaseID   int64             `json:"baseId"`
	Document directorDocument  `json:"document"`
	Issues   []director.Issue  `json:"issues"`
	Changes  []director.Change `json:"changes"`
	Hash     string            `json:"hash"`
}

func directorImportRows(ctx context.Context, tx pgx.Tx, p directorPeriod, input directorEdit) ([]director.Row, error) {
	if len(input.File) == 0 || len(input.File) > 10<<20 || len(input.FileName) > 255 || filepath.Base(input.FileName) != input.FileName || !strings.EqualFold(filepath.Ext(input.FileName), ".xlsx") || len(input.Bindings) > 5000 {
		return nil, invalid
	}
	rows, err := director.Parse(bytes.NewReader(input.File))
	if err != nil {
		return nil, fmt.Errorf("%w：%s", invalid, err)
	}
	numbers := map[int]bool{}
	var names, ids []string
	for _, r := range rows {
		numbers[r.Number] = true
		names = append(names, r.LeaderCompanyName, r.MemberCompanyName)
	}
	bindings := map[int]*directorBinding{}
	for i := range input.Bindings {
		b := &input.Bindings[i]
		if !numbers[b.Number] || bindings[b.Number] != nil || (b.Pending && b.Member != "") {
			return nil, fmt.Errorf("%w：序号或待核对选择无效", invalid)
		}
		bindings[b.Number] = b
		ids = append(ids, b.Company, b.Member)
	}
	directory, err := readDirectorDirectory(ctx, tx, p.Start, names, ids)
	if err != nil {
		return nil, err
	}
	for i, r := range rows {
		rows[i] = directory.bind(r, bindings[r.Number])
	}
	return rows, nil
}
func directorManualRows(ctx context.Context, tx pgx.Tx, p directorPeriod, rows []director.Row, input directorEdit) ([]director.Row, error) {
	id, ok := canonicalSourceUUID(input.Company)
	if !ok {
		return nil, fmt.Errorf("%w：请选择销售公司", invalid)
	}
	d, err := readDirectorDirectory(ctx, tx, p.Start, nil, []string{id})
	if err != nil {
		return nil, err
	}
	c, found := d.companies[id]
	if !found {
		return nil, fmt.Errorf("%w：请选择内部或外部销售公司", invalid)
	}
	switch input.Kind {
	case "director":
		if !textOK(input.Department, 100) || c.Status != "ACTIVE" || input.Person == "" {
			return nil, fmt.Errorf("%w：请填写表内部门并选择启用公司的总监账号", invalid)
		}
		for _, r := range rows {
			if r.LeaderCompanyID == id {
				return nil, fmt.Errorf("%w：该公司已设为总监公司", conflict)
			}
		}
		a := d.person(id, input.Person, "")
		recipient, _ := directorPayment(c, a)
		if recipient == "" {
			return nil, fmt.Errorf("%w：请选择开班时属于该公司且有唯一之道账号的总监", invalid)
		}
		r := director.Row{Number: director.NextNumber(rows), Department: strings.TrimSpace(input.Department), LeaderName: a.Name, LeaderCompanyName: c.Name, DirectorOnly: true}
		return append(rows, d.bind(r, &directorBinding{Company: id, Leader: a.ID})), nil
	case "member":
		if input.Director != "" && c.Category != "幸福驿站" {
			return nil, fmt.Errorf("%w：只有幸福驿站销售公司可设置总监归属", invalid)
		}
		next, err := director.UpdateMember(rows, id, c.Name, input.Director, c.Source)
		if err != nil {
			return nil, fmt.Errorf("%w：%s", invalid, err)
		}
		if next == nil {
			return nil, fmt.Errorf("%w：该公司的总监归属没有变化", invalid)
		}
		return next, nil
	}
	return nil, invalid
}

func buildDirectorPreview(ctx context.Context, tx pgx.Tx, p directorPeriod, req directorRequest) (v directorPreview, err error) {
	v.Period = p
	baseID, base, err := readDirectorBase(ctx, tx, p.ProductID)
	if err != nil {
		return v, err
	}
	v.BaseID = baseID
	if req.BaseID != baseID {
		return v, fmt.Errorf("%w：总监版本已更新，请刷新后重新核对", conflict)
	}
	if p.Start.Before(base.StartsAt) && p.Key != base.PeriodKey {
		return v, fmt.Errorf("%w：新版起始期次不能早于最新版本", invalid)
	}
	var rows []director.Row
	switch req.Edit.Kind {
	case "legacy":
		rows, v.Issues, v.Document.SourceVersion, err = directorLegacyRows(ctx, tx, p, req.Edit)
	case "import":
		rows, err = directorImportRows(ctx, tx, p, req.Edit)
	case "director", "member":
		rows, err = directorManualRows(ctx, tx, p, base.Rows, req.Edit)
	default:
		return v, invalid
	}
	if err != nil {
		return v, err
	}
	if len(rows) == 0 || len(rows) > 5000 {
		return v, fmt.Errorf("%w：归属关系须为1至5000条", invalid)
	}
	v.Document = directorDocument{Version: base.Version + 1, PeriodKey: p.Key, PeriodName: p.Name, StartsAt: p.Start, Rows: rows, SourceVersion: v.Document.SourceVersion}
	v.Issues = append(v.Issues, director.Validate(rows)...)
	v.Changes = director.Diff(base.Rows, rows)
	v.Hash = director.Hash(struct {
		Period   directorPeriod
		Base     int64
		EditHash string
		Rows     []director.Row
	}{p, baseID, director.Hash(req.Edit), rows})
	return v, nil
}

func (s workspace) previewDirectorEdit(ctx context.Context, req directorRequest) (v directorPreview, err error) {
	if _, err = s.appointmentOperator(ctx); err != nil {
		return
	}
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		p, err := directorPeriodFor(ctx, tx, req.PeriodID)
		if err != nil {
			return err
		}
		v, err = buildDirectorPreview(ctx, tx, p, req)
		return err
	})
	return
}
func directorSourceEvidence(edit directorEdit) (name string, data []byte) {
	if edit.Kind == "import" || edit.Kind == "legacy" {
		return edit.FileName, edit.File
	}
	if edit.Kind == "director" {
		return "登记总监", directorMarshal(edit)
	}
	return "调整公司归属", directorMarshal(edit)
}
func (s workspace) saveDirectorEdit(ctx context.Context, req directorRequest) (id int64, err error) {
	requestID, ok := canonicalSourceUUID(req.RequestID)
	if !ok || len(req.Expected) != 71 {
		return 0, invalid
	}
	err = s.writeAppointments(ctx, func(tx pgx.Tx, actor *string) error {
		p, err := directorPeriodFor(ctx, tx, req.PeriodID)
		if err != nil {
			return err
		}
		var product int64
		if err = tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 FOR NO KEY UPDATE`, p.ProductID).Scan(&product); err != nil {
			return err
		}
		fileName, source := directorSourceEvidence(req.Edit)
		fileHash := directorRawHash(source)
		var same bool
		err = tx.QueryRow(ctx, `SELECT id,period_id=$3 AND file_hash=$4 AND preview_hash=$5 AND created_by IS NOT DISTINCT FROM $6::uuid
 AND request_hash=$7 FROM practice_director_versions WHERE product_id=$1 AND request_id=$2::uuid`, product, requestID, p.ID, fileHash, req.Expected, actor, director.Hash(req.Edit)).Scan(&id, &same)
		if err == nil {
			if !same {
				return conflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		p, err = directorPeriodFor(ctx, tx, req.PeriodID)
		if err != nil {
			return err
		}
		v, err := buildDirectorPreview(ctx, tx, p, req)
		if err != nil {
			return err
		}
		if v.Hash != req.Expected || len(v.Issues) != 0 {
			return fmt.Errorf("%w：依据已变化或仍有未解决的匹配，请重新核对", conflict)
		}
		if err = tx.QueryRow(ctx, `INSERT INTO practice_director_versions(product_id,period_id,starts_at,version_no,document,file_name,source_file,file_hash,preview_hash,request_hash,request_id,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::uuid,$12::uuid) RETURNING id`, product, p.ID, p.Start, v.Document.Version, v.Document, fileName, source, fileHash, v.Hash, director.Hash(req.Edit), requestID, actor).Scan(&id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE products SET director_registry_revision=director_registry_revision+1 WHERE id=$1`, product); err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, `SELECT p.id FROM (`+directorPeriodsSQL+`) family
 JOIN periods p ON p.id=family.id JOIN LATERAL (SELECT rv.components FROM rule_versions rv
 CROSS JOIN LATERAL (SELECT `+componentRulePeriodEndSQL+` AS at) stopped
 WHERE `+matchingRuleScope+` AND (rv.effective_from IS NULL OR rv.effective_from<=family.starts_at)
 AND (stopped.at IS NULL OR family.starts_at<stopped.at) ORDER BY `+matchingRuleOrder+` LIMIT 1) rule ON true
 WHERE family.product_id=$1 AND family.starts_at>=least($2::timestamptz,(SELECT starts_at FROM practice_director_versions
 WHERE id=$3 AND period_id=$4)) AND rule.components @>'[{"mode":"director_rate"}]'::jsonb`, product, p.Start, v.BaseID, p.ID)
	})
	return
}

func (s workspace) directorDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id < 1 {
		respond(w, nil, invalid)
		return
	}
	var result map[string]json.RawMessage
	var current directorDocument
	var previous []director.Row
	err = s.db.QueryRow(r.Context(), `SELECT to_jsonb(summary),d.document,coalesce((SELECT v.document->'rows' FROM practice_director_versions v
 WHERE v.product_id=d.product_id AND v.version_no<d.version_no ORDER BY v.version_no DESC LIMIT 1),'[]')
 FROM (`+directorVersionSummarySQL+`) summary JOIN practice_director_versions d USING(id) WHERE id=$1`, id).Scan(&result, &current, &previous)
	if err == nil {
		result["document"] = directorMarshal(current)
		result["changes"] = directorMarshal(director.Diff(previous, current.Rows))
	}
	respond(w, result, err)
}
