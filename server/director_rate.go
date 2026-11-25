package main

import (
	"errors"
	"fmt"
	"slices"
)

const directorRateMode = "director_rate"

var errDirectorRateScope = errors.New("同一报名类型只能使用一套总监归属，请核对按期沿用和已导入的总监安排")

func usesDirectorRegistry(parts []component) bool {
	return slices.ContainsFunc(parts, func(c component) bool { return c.Mode == directorRateMode })
}

// The register freezes both local payees and source accounts when finance saves
// a version. A subsequent identity merge or rename must not change a refund.
func directorRateBenefits(doc practiceRegistrationDocument) ([]component, error) {
	if len(doc.Rows) == 0 {
		return nil, nil
	}
	companies := make(map[string]string)
	people := make(map[string]int64)
	for _, row := range doc.Rows {
		companies[row.SourceCompany] = row.LeaderCompany
		if row.SourceMember != "" {
			if _, exists := companies[row.SourceMember]; !exists {
				companies[row.SourceMember] = ""
			}
		}
		if row.Recipient == row.LeaderID {
			people[row.SourceSeller] = positiveSourceID(row.LeaderID)
		}
	}
	preview := buildPracticeRegistrationImport(doc, 0, companies, people)
	if len(preview.Issues) != 0 {
		return nil, fmt.Errorf("总监归属第 %d 行：%s", preview.Issues[0].Number, preview.Issues[0].Message)
	}
	return preview.Components, nil
}

func checkDirectorRateScopes(items []component) error {
	if !usesDirectorRegistry(items) {
		return nil
	}
	for _, member := range []string{"new", "retrain"} {
		dynamic, fixed := 0, false
		for _, part := range items {
			if !part.matchesPracticeMember(member) {
				continue
			}
			if part.Mode == directorRateMode {
				dynamic++
			}
			if b := part.PeriodBenefit; b != nil && b.RegistrationScope != nil && b.RegistrationScope.Exclusive {
				fixed = true
			}
		}
		if dynamic > 1 || dynamic == 1 && fixed {
			return errDirectorRateScope
		}
	}
	return nil
}
