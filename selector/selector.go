// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package selector реализует парсер и SQL-генератор для Watch/List-селекторов Kachō.
//
// Логика комбинирования:
//   - Внутри одного Selector: field_selector AND label_selector (все поля совпадают).
//   - Между Selector-ами: OR (ресурс попадает, если соответствует хотя бы одному).
package selector

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FieldFilter содержит поля для фильтрации по полям метаданных ресурса.
type FieldFilter struct {
	Name           string
	OrganizationID string
	CloudID        string
	FolderID       string
}

// Selector описывает один элемент условия фильтрации.
// Поля field и label объединяются через AND.
type Selector struct {
	// Field — фильтр по полям метаданных (AND между заполненными полями).
	Field *FieldFilter
	// Labels — фильтр по меткам (containment: ресурс должен содержать все указанные метки).
	Labels map[string]string
}

// BuildResult содержит SQL WHERE clause и параметры для него.
type BuildResult struct {
	// WhereClause — SQL fragment, например "WHERE (name = $1) OR (cloud_id = $2)".
	// Пустая строка если фильтры не заданы.
	WhereClause string
	// Args — параметры для подстановки ($1, $2, ...).
	Args []any
}

// Build генерирует SQL WHERE clause из списка Selector-ов.
// Защита от SQL injection: только параметризованные значения, без конкатенации.
// При пустом списке возвращает BuildResult с пустым WhereClause и nil Args.
func Build(selectors []Selector) (BuildResult, error) {
	return BuildFrom(selectors, 1)
}

// BuildFrom генерирует SQL WHERE clause, начиная нумерацию параметров с startParam.
// Удобно для построения сложных запросов с несколькими блоками параметров.
func BuildFrom(selectors []Selector, startParam int) (BuildResult, error) {
	if len(selectors) == 0 {
		return BuildResult{}, nil
	}

	var args []any
	var orParts []string
	paramIdx := startParam

	for _, sel := range selectors {
		andParts, newArgs, newIdx, err := buildOne(sel, paramIdx)
		if err != nil {
			return BuildResult{}, err
		}
		args = append(args, newArgs...)
		paramIdx = newIdx

		if len(andParts) == 0 {
			// Пустой selector без условий — матчит все (эквивалент WHERE TRUE).
			// Если хотя бы один selector матчит все — весь WHERE пуст.
			return BuildResult{}, nil
		}

		clause := strings.Join(andParts, " AND ")
		orParts = append(orParts, "("+clause+")")
	}

	if len(orParts) == 0 {
		return BuildResult{}, nil
	}

	var where string
	if len(orParts) == 1 {
		where = "WHERE " + orParts[0]
	} else {
		where = "WHERE (" + strings.Join(orParts, " OR ") + ")"
	}

	return BuildResult{WhereClause: where, Args: args}, nil
}

// buildOne строит AND-части для одного Selector.
func buildOne(sel Selector, startParam int) (andParts []string, args []any, nextParam int, err error) {
	paramIdx := startParam

	if sel.Field != nil {
		f := sel.Field
		if f.Name != "" {
			andParts = append(andParts, fmt.Sprintf("name = $%d", paramIdx))
			args = append(args, f.Name)
			paramIdx++
		}
		if f.OrganizationID != "" {
			andParts = append(andParts, fmt.Sprintf("organization_id = $%d", paramIdx))
			args = append(args, f.OrganizationID)
			paramIdx++
		}
		if f.CloudID != "" {
			andParts = append(andParts, fmt.Sprintf("cloud_id = $%d", paramIdx))
			args = append(args, f.CloudID)
			paramIdx++
		}
		if f.FolderID != "" {
			andParts = append(andParts, fmt.Sprintf("folder_id = $%d", paramIdx))
			args = append(args, f.FolderID)
			paramIdx++
		}
	}

	if len(sel.Labels) > 0 {
		labelJSON, jerr := json.Marshal(sel.Labels)
		if jerr != nil {
			err = fmt.Errorf("marshal labels: %w", jerr)
			return
		}
		andParts = append(andParts, fmt.Sprintf("labels @> $%d::jsonb", paramIdx))
		args = append(args, string(labelJSON))
		paramIdx++
	}

	nextParam = paramIdx
	return andParts, args, nextParam, nil
}

// PageToken используется для keyset pagination.
type PageToken struct {
	LastResourceVersion int64  `json:"last_rv"`
	LastUID             string `json:"last_uid"`
}

// BuildPageClause генерирует SQL-условие для keyset pagination.
// Возвращает "" если token пустой (первая страница).
func BuildPageClause(token *PageToken, startParam int) (clause string, args []any) {
	if token == nil {
		return "", nil
	}
	clause = fmt.Sprintf(
		"(resource_version, uid) > ($%d, $%d)",
		startParam, startParam+1,
	)
	args = []any{token.LastResourceVersion, token.LastUID}
	return clause, args
}
