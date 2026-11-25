package director

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

var (
	ErrInvalid  = errors.New("总监归属内容不正确")
	ErrConflict = errors.New("总监归属依据已变化，请重新核对")

	Headers = []string{"序号", "部门", "组长/总监", "组长销售公司", "组员姓名", "下属公司名称"}
)

type Row struct {
	Number                   int    `json:"number"`
	Department               string `json:"department"`
	LeaderName               string `json:"leader_name"`
	LeaderCompanyName        string `json:"leader_company_name"`
	MemberName               string `json:"member_name"`
	MemberCompanyName        string `json:"member_company_name"`
	LeaderID                 string `json:"leader_id"`
	LeaderCompanyID          string `json:"leader_company_id"`
	MemberCompanyID          string `json:"member_company_id"`
	RecipientID              string `json:"recipient_id"`
	LeaderSourceUserID       string `json:"leader_source_user_id"`
	LeaderSourceCompanyID    string `json:"leader_source_company_id"`
	MemberSourceCompanyID    string `json:"member_source_company_id"`
	PendingReview            bool   `json:"pending_review,omitempty"`
	DirectorOnly             bool   `json:"director_only,omitempty"`
	PendingCompanyID         string `json:"pending_company_id,omitempty"`
	LeaderDisplayName        string `json:"leader_display_name,omitempty"`
	LeaderCompanyDisplayName string `json:"leader_company_display_name,omitempty"`
	MemberCompanyDisplayName string `json:"member_company_display_name,omitempty"`
	RecipientDisplayName     string `json:"recipient_display_name,omitempty"`
}
type Issue struct {
	Number int    `json:"number"`
	Detail string `json:"detail"`
}
type Change struct {
	Company string `json:"company"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Kind    string `json:"kind"`
}

func Hash(v any) string {
	raw, _ := json.Marshal(v)
	s := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(s[:])
}

func Parse(input io.Reader) ([]Row, error) {
	f, err := excelize.OpenReader(input, excelize.Options{UnzipSizeLimit: 40 << 20, UnzipXMLSizeLimit: 10 << 20})
	if err != nil {
		return nil, fmt.Errorf("%w：请上传有效的xlsx文件", ErrInvalid)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) != 1 {
		return nil, fmt.Errorf("%w：关系表须只有一个工作表", ErrInvalid)
	}
	it, err := f.Rows(sheets[0])
	if err != nil {
		return nil, err
	}
	defer it.Close()
	rows := []Row{}
	line := 0
	for it.Next() {
		line++
		values, err := it.Columns()
		if err != nil {
			return nil, err
		}
		for i := range values {
			cell, _ := excelize.CoordinatesToCellName(i+1, line)
			typ, err := f.GetCellType(sheets[0], cell)
			if err != nil || (typ != excelize.CellTypeUnset && typ != excelize.CellTypeSharedString && typ != excelize.CellTypeInlineString && typ != excelize.CellTypeNumber) {
				return nil, fmt.Errorf("%w：第%d行须使用文字或数值", ErrInvalid, line)
			}
		}
		if line == 1 {
			if len(values) != 6 {
				return nil, fmt.Errorf("%w：表头须为原模板的六列", ErrInvalid)
			}
			for i := range Headers {
				if strings.TrimSpace(values[i]) != Headers[i] {
					return nil, fmt.Errorf("%w：第%d列表头应为%s", ErrInvalid, i+1, Headers[i])
				}
			}
			continue
		}
		if line > 5002 {
			return nil, fmt.Errorf("%w：最多上传5000条关系", ErrInvalid)
		}
		blank := true
		for _, v := range values {
			if strings.TrimSpace(v) != "" {
				blank = false
			}
		}
		if blank {
			continue
		}
		if len(values) != 6 || len(rows) >= 5000 {
			return nil, fmt.Errorf("%w：第%d行须填写完整六列", ErrInvalid, line)
		}
		for i := range values {
			values[i] = strings.TrimSpace(values[i])
			cell, _ := excelize.CoordinatesToCellName(i+1, line)
			formula, err := f.GetCellFormula(sheets[0], cell)
			if err != nil || formula != "" {
				return nil, fmt.Errorf("%w：第%d行须使用文字或数值，不能使用公式", ErrInvalid, line)
			}
			if values[i] == "" || len([]rune(values[i])) > 255 {
				return nil, fmt.Errorf("%w：第%d行存在空值或过长内容", ErrInvalid, line)
			}
		}
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 {
			return nil, fmt.Errorf("%w：第%d行序号应为正整数", ErrInvalid, line)
		}
		rows = append(rows, Row{Number: n, Department: values[1], LeaderName: values[2], LeaderCompanyName: values[3], MemberName: values[4], MemberCompanyName: values[5]})
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w：关系表没有数据", ErrInvalid)
	}
	return rows, nil
}

func Validate(rows []Row) []Issue {
	issues := []Issue{}
	numbers := map[int]bool{}
	members := map[string]bool{}
	memberNames := map[string]bool{}
	memberSources := map[string]bool{}
	leaderSources := map[string]string{}
	companySources := map[string]bool{}
	leaders := map[string]Row{}
	companies := map[string]string{}
	for _, r := range rows {
		add := func(s string) { issues = append(issues, Issue{r.Number, s}) }
		if numbers[r.Number] {
			add("序号重复")
		}
		numbers[r.Number] = true
		if r.LeaderID == "" {
			add("请选择唯一的总监本人")
		}
		if r.LeaderID != "" && r.LeaderSourceUserID == "" {
			add("总监本人的业务销售账号缺失或不唯一")
		}
		if r.LeaderCompanyID != "" && r.LeaderSourceCompanyID == "" {
			add("总监销售公司的业务标识缺失或不唯一")
		}
		if r.MemberCompanyID != "" && r.MemberSourceCompanyID == "" {
			add("下属公司的业务标识缺失或不唯一")
		}
		if r.DirectorOnly && (r.PendingReview || r.MemberCompanyID != "" || r.PendingCompanyID != "" || r.MemberSourceCompanyID != "") {
			add("总监档案行不能绑定下属公司")
		}
		if r.PendingCompanyID != "" && !r.PendingReview {
			add("待核对公司档案必须标记为待核对")
		}
		if r.PendingReview && (r.MemberCompanyID != "" || r.MemberSourceCompanyID != "") {
			add("待核对关系不能同时绑定下属公司，请先核对后补关联")
		}
		if !r.DirectorOnly && r.PendingCompanyID == "" {
			nameKey := strings.TrimSpace(r.MemberCompanyName)
			if pending, seen := memberNames[nameKey]; seen && (pending || r.PendingReview) {
				add("同名下属公司存在重复待核对记录，请合并核对")
			}
			memberNames[nameKey] = memberNames[nameKey] || r.PendingReview
		}
		if r.MemberSourceCompanyID != "" {
			if memberSources[r.MemberSourceCompanyID] {
				add("同一业务公司重复列为下属公司")
			}
			memberSources[r.MemberSourceCompanyID] = true
		}
		if r.LeaderSourceCompanyID != "" {
			companySources[r.LeaderSourceCompanyID] = true
		}
		if r.LeaderSourceUserID != "" {
			if before, ok := leaderSources[r.LeaderSourceUserID]; ok && before != r.LeaderID {
				add("同一业务销售账号绑定到多个总监档案")
			}
			leaderSources[r.LeaderSourceUserID] = r.LeaderID
		}
		if r.LeaderCompanyID == "" {
			add("请匹配总监销售公司")
		}
		if r.MemberCompanyID == "" && !r.PendingReview && !r.DirectorOnly {
			add("请匹配下属公司")
		}
		if r.RecipientID == "" || (r.RecipientID != r.LeaderID && r.RecipientID != r.LeaderCompanyID) {
			add("提成受益方须为总监本人或其销售公司")
		}
		if id := memberPartyID(r); id != "" {
			if members[id] {
				add("下属公司重复或分配给多个总监")
			}
			members[id] = true
		}
		if r.LeaderID != "" {
			if before, ok := leaders[r.LeaderID]; ok && (before.Department != r.Department || before.LeaderCompanyID != r.LeaderCompanyID || before.RecipientID != r.RecipientID || before.LeaderSourceUserID != r.LeaderSourceUserID || before.LeaderSourceCompanyID != r.LeaderSourceCompanyID) {
				add("同一总监的部门、销售公司和受益方须一致")
			}
			leaders[r.LeaderID] = r
		}
		if r.LeaderCompanyID != "" {
			if before, ok := companies[r.LeaderCompanyID]; ok && before != r.LeaderID {
				add("同一总监销售公司对应多人，请核对本人账号")
			}
			companies[r.LeaderCompanyID] = r.LeaderID
		}
	}
	for _, r := range rows {
		if _, ok := companies[memberPartyID(r)]; ok {
			issues = append(issues, Issue{r.Number, "总监销售公司不能同时列为下属公司"})
		}
		if companySources[r.MemberSourceCompanyID] && r.MemberSourceCompanyID != "" {
			issues = append(issues, Issue{r.Number, "总监销售公司不能同时作为其他总监的下属公司"})
		}
	}
	return issues
}

func memberPartyID(row Row) string {
	if row.MemberCompanyID != "" {
		return row.MemberCompanyID
	}
	return row.PendingCompanyID
}

func Diff(before, after []Row) []Change {
	key := func(r Row) string {
		if id := memberPartyID(r); id != "" {
			return "company:" + id
		}
		return "pending:" + Hash(r.MemberCompanyName)
	}
	index := func(rows []Row) map[string]Row {
		m := map[string]Row{}
		for _, r := range rows {
			if r.DirectorOnly {
				continue
			}
			m[key(r)] = r
		}
		return m
	}
	a, b := index(before), index(after)
	used := map[string]bool{}
	out := []Change{}
	for _, r := range after {
		if r.DirectorOnly {
			continue
		}
		oldKey := key(r)
		old, ok := a[oldKey]
		if !ok && !r.PendingReview {
			pendingKey := "pending:" + Hash(r.MemberCompanyName)
			if pending, found := a[pendingKey]; found && pending.PendingReview {
				old, ok, oldKey = pending, true, pendingKey
			}
		}
		kind := "新增"
		if r.PendingReview {
			kind = "待核对"
		}
		if ok {
			used[oldKey] = true
			if old.PendingReview == r.PendingReview && old.LeaderID == r.LeaderID && old.RecipientID == r.RecipientID && old.LeaderCompanyID == r.LeaderCompanyID && old.LeaderSourceUserID == r.LeaderSourceUserID && old.LeaderSourceCompanyID == r.LeaderSourceCompanyID && old.MemberSourceCompanyID == r.MemberSourceCompanyID {
				continue
			}
			kind = "转归"
			if old.LeaderID == r.LeaderID && old.RecipientID == r.RecipientID {
				kind = "账号调整"
			}
			if old.PendingReview && !r.PendingReview {
				kind = "补关联"
			}
		}
		out = append(out, Change{r.MemberCompanyName, old.LeaderName, r.LeaderName, kind})
	}
	for _, r := range before {
		if r.DirectorOnly {
			continue
		}
		if _, ok := b[key(r)]; !ok && !used[key(r)] {
			out = append(out, Change{r.MemberCompanyName, r.LeaderName, "", "移出"})
		}
	}
	// Standalone directors have no member-company key. Include their own
	// account in the audit when the director first appears, changes or leaves.
	leaders := func(rows []Row) map[string]Row {
		all := map[string]Row{}
		for _, row := range rows {
			all[row.LeaderCompanyID] = row
		}
		return all
	}
	priorLeaders, nextLeaders := leaders(before), leaders(after)
	seen := map[string]bool{}
	for _, row := range after {
		if !row.DirectorOnly || seen[row.LeaderCompanyID] {
			continue
		}
		seen[row.LeaderCompanyID] = true
		old, ok := priorLeaders[row.LeaderCompanyID]
		kind := "登记总监"
		if ok {
			if old.LeaderID == row.LeaderID && old.LeaderSourceUserID == row.LeaderSourceUserID && old.RecipientID == row.RecipientID && old.LeaderSourceCompanyID == row.LeaderSourceCompanyID {
				continue
			}
			kind = "总监账号调整"
		}
		out = append(out, Change{row.LeaderCompanyName, old.LeaderName, row.LeaderName, kind})
	}
	for _, row := range before {
		if !row.DirectorOnly || seen[row.LeaderCompanyID] {
			continue
		}
		seen[row.LeaderCompanyID] = true
		if _, ok := nextLeaders[row.LeaderCompanyID]; !ok {
			out = append(out, Change{row.LeaderCompanyName, row.LeaderName, "", "移出总监"})
		}
	}
	return out
}
func directorRow(row Row) Row {
	row.DirectorOnly = true
	row.MemberCompanyID, row.MemberCompanyName, row.MemberCompanyDisplayName = "", "", ""
	row.MemberSourceCompanyID, row.MemberName = "", ""
	row.PendingCompanyID, row.PendingReview = "", false
	return row
}

func NextNumber(rows []Row) int {
	n := 0
	for _, row := range rows {
		n = max(n, row.Number)
	}
	return n + 1
}

// A nil result keeps the current relationship version.
func UpdateMember(rows []Row, companyID, companyName, directorID, source string) ([]Row, error) {
	var director, oldMember Row
	for _, r := range rows {
		if r.LeaderCompanyID == directorID {
			director = r
		}
		if memberPartyID(r) == companyID {
			oldMember = r
		}
	}
	if directorID != "" && director.LeaderCompanyID == "" {
		return nil, fmt.Errorf("%w：请选择已配置的总监公司", ErrInvalid)
	}
	if oldMember.LeaderCompanyID == directorID && (directorID == "" || oldMember.MemberSourceCompanyID == source) {
		return nil, nil
	}
	next := NextNumber(rows)
	out := make([]Row, 0, len(rows)+1)
	for _, row := range rows {
		if memberPartyID(row) != companyID {
			out = append(out, row)
		}
	}
	if directorID != "" {
		row := directorRow(director)
		row.Number = next
		if oldMember.Number != 0 {
			row.Number, row.MemberName = oldMember.Number, oldMember.MemberName
		}
		row.DirectorOnly = false
		row.MemberCompanyName, row.MemberCompanyDisplayName = companyName, companyName
		row.MemberCompanyID, row.MemberSourceCompanyID = companyID, source
		if source == "" {
			row.MemberCompanyID, row.PendingCompanyID, row.PendingReview = "", companyID, true
		}
		out = append(out, row)
	}
	if oldMember.Number != 0 && !slices.ContainsFunc(out, func(r Row) bool { return r.LeaderCompanyID == oldMember.LeaderCompanyID }) {
		row := directorRow(oldMember)
		row.Number = next
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b Row) int { return a.Number - b.Number })
	return out, nil
}
