package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errPracticeOpening         = errors.New("请核对冻结结算编号、截止时间及所有人员的期初人数")
	errPracticeOpeningScope    = errors.New("所选冻结期初不属于该产品")
	errPracticeOpeningConflict = errors.New("同一冻结结算已导入不同的期初")
)

type practiceCountOpeningFile struct {
	ProductID          int64                        `json:"productId"`
	SourceSettlementID string                       `json:"sourceSettlementId"`
	ThroughAt          time.Time                    `json:"throughAt"`
	People             []practiceCountOpeningPerson `json:"people"`
}

type practiceCountOpeningPerson struct {
	LegacyPartyID string `json:"legacyPartyId"`
	DisplayName   string `json:"displayName,omitempty"`
	NewCount      *int64 `json:"newCount"`
}

type practiceCountOpening struct {
	throughAt time.Time
	counts    map[int64]int64
}

const practiceCountOpeningsSQL = `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a."throughAt" DESC,a.id DESC),'[]') FROM (
 SELECT o.id,o.product_id AS "productId",o.source_settlement_id AS "sourceSettlementId",o.through_at AS "throughAt",
 (SELECT count(*) FROM practice_count_opening_people p WHERE p.opening_id=o.id) AS "personCount"
 FROM practice_count_openings o ORDER BY o.through_at DESC,o.id DESC
) a`

func canonicalSourceUUID(raw string) (string, bool) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil || !id.Valid || id.Bytes == [16]byte{} {
		return "", false
	}
	return id.String(), true
}

func openingContent(input *practiceCountOpeningFile) ([]byte, error) {
	id, valid := canonicalSourceUUID(input.SourceSettlementID)
	if !valid || input.ProductID < 1 || input.ThroughAt.IsZero() || len(input.People) == 0 {
		return nil, errPracticeOpening
	}
	input.SourceSettlementID = id
	input.ThroughAt = input.ThroughAt.UTC().Truncate(time.Microsecond)
	input.People = append([]practiceCountOpeningPerson(nil), input.People...)
	counts := make(map[string]int64, len(input.People))
	for i := range input.People {
		row := &input.People[i]
		key, valid := canonicalSourceUUID(row.LegacyPartyID)
		row.DisplayName = strings.TrimSpace(row.DisplayName)
		_, duplicate := counts[key]
		if !valid || duplicate || row.NewCount == nil || *row.NewCount < 0 ||
			row.DisplayName != "" && !textOK(row.DisplayName, 80) {
			return nil, errPracticeOpening
		}
		row.LegacyPartyID = key
		counts[key] = *row.NewCount
	}
	payload, err := json.Marshal(struct {
		ThroughAt time.Time
		Counts    map[string]int64
	}{input.ThroughAt, counts})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	return digest[:], nil
}

const openingIncomingRows = `jsonb_to_recordset($1::jsonb) AS incoming("legacyPartyId" uuid,"displayName" text,"newCount" bigint)`

func storePracticeCountOpening(ctx context.Context, tx pgx.Tx, input practiceCountOpeningFile) (int64, bool, error) {
	digest, err := openingContent(&input)
	if err != nil {
		return 0, false, err
	}
	var periodMode string
	if err := tx.QueryRow(ctx, "SELECT period_mode FROM products WHERE id=$1 FOR UPDATE", input.ProductID).Scan(&periodMode); err != nil {
		return 0, false, err
	}
	if periodMode != "class" {
		return 0, false, errPracticeOpeningScope
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO practice_count_openings(product_id,source_settlement_id,through_at,source_digest)
	 VALUES($1,$2::uuid,$3,$4) ON CONFLICT(source_settlement_id) DO NOTHING RETURNING id`,
		input.ProductID, input.SourceSettlementID, input.ThroughAt, digest).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var storedDigest []byte
		var productID int64
		err = tx.QueryRow(ctx, `SELECT id,product_id,source_digest FROM practice_count_openings WHERE source_settlement_id=$1::uuid`,
			input.SourceSettlementID).Scan(&id, &productID, &storedDigest)
		if err != nil {
			return 0, false, err
		}
		if productID != input.ProductID || !bytes.Equal(storedDigest, digest) {
			return 0, false, errPracticeOpeningConflict
		}
		return id, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	raw, err := json.Marshal(input.People)
	if err != nil {
		return 0, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO people(name,legacy_party_id)
	 SELECT incoming."displayName",incoming."legacyPartyId" FROM `+openingIncomingRows+`
	 WHERE nullif(incoming."displayName",'') IS NOT NULL ORDER BY incoming."legacyPartyId"
	 ON CONFLICT(legacy_party_id) DO NOTHING`, raw); err != nil {
		return 0, false, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO practice_count_opening_people(opening_id,person_id,new_count)
	 SELECT $2,p.id,incoming."newCount" FROM `+openingIncomingRows+` JOIN people p ON p.legacy_party_id=incoming."legacyPartyId"`,
		raw, id)
	if err != nil {
		return 0, false, err
	}
	if result.RowsAffected() != int64(len(input.People)) {
		return 0, false, errPracticeCountPerson
	}
	return id, true, nil
}

func importPracticeCountOpening(ctx context.Context, cfg config, filename string) error {
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("opening import requires the isolated development database")
	}
	var input practiceCountOpeningFile
	if err := readImportFile(filename, &input, 8<<20); err != nil {
		return err
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("could not initialize the isolated database")
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return err
	}
	var id int64
	var inserted bool
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var writeErr error
		id, inserted, writeErr = storePracticeCountOpening(ctx, tx, input)
		return writeErr
	})
	if err != nil {
		return err
	}
	fmt.Printf("Opening %d, %d people, imported: %t\n", id, len(input.People), inserted)
	return nil
}

func readPracticeCountOpening(ctx context.Context, tx pgx.Tx, openingID, periodID int64) (practiceCountOpening, error) {
	opening := practiceCountOpening{counts: make(map[int64]int64)}
	err := tx.QueryRow(ctx, `SELECT o.through_at FROM practice_count_openings o JOIN periods p ON p.product_id=o.product_id
	 WHERE o.id=$1 AND p.id=$2`, openingID, periodID).Scan(&opening.throughAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return opening, errPracticeOpeningScope
	}
	if err != nil {
		return opening, err
	}
	rows, err := tx.Query(ctx, "SELECT person_id,new_count FROM practice_count_opening_people WHERE opening_id=$1", openingID)
	if err != nil {
		return opening, err
	}
	defer rows.Close()
	for rows.Next() {
		var person, count int64
		if err := rows.Scan(&person, &count); err != nil {
			return opening, err
		}
		opening.counts[person] = count
	}
	return opening, rows.Err()
}
