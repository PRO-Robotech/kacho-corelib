// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package filter — простой парсер filter-выражений API Kachō.
//
// Текущая поддержка:
//
//	<field> = "<value>"
//
// Где <field> — whitelisted set (например "name"), <value> — double-quoted
// строка. Возвращает (FilterAST, error). FilterAST использует SQL-binding
// (без string concat) при превращении в WHERE clause.
//
// Формат сообщений об ошибках:
//
//	"Bad expression at column N. Unknown field: \"<field>\""
//	"Bad expression at column N. Expected an operator"
//	"Bad expression at column N. Expected a string, integer, date-time or boolean value"
//
// Поддержка AND/OR/STARTS_WITH/IN — отложена.
package filter

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// safeFieldRe — идентификатор-колонка, безопасная для дословной подстановки в SQL:
// ровно то подмножество символов, которое эмитит Parse (буквы/цифры/подчёркивание/
// точка, начиная с буквы или подчёркивания). Любое отклонение (пробел, кавычка,
// оператор, скобка) означает, что Field сконструирован в обход Parse-whitelist'а и
// подлежит защитному quoting'у.
var safeFieldRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)

// FilterAST — узел AST. Для текущего узла-минимума: одно equals.
type FilterAST struct {
	Field string
	Op    string // "="
	Value string
}

// ParseError — ошибка парсинга с message в фиксированном формате.
type ParseError struct {
	Column  int
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("Bad expression at column %d. %s", e.Column, e.Message)
}

// Parse разбирает filter-выражение.
// allowedFields — whitelist полей.
//
// Возвращает (nil, nil) для пустого input — означает "no filter".
// Возвращает *FilterAST или *ParseError.
func Parse(input string, allowedFields []string) (*FilterAST, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	// 1. Извлекаем имя поля. Идентификатор: первый символ — буква или '_',
	// далее — буквы/цифры/'_'/'.'. Тот же набор, что принимает safeFieldRe,
	// поэтому легитимное поле дословно проходит ToSQL без защитного quoting'а.
	col := 1
	i := 0
	fieldStart := i
	if i < len(input) && (isLetter(input[i]) || input[i] == '_') {
		i++
		for i < len(input) && (isAlphaNum(input[i]) || input[i] == '_' || input[i] == '.') {
			i++
		}
	}
	field := input[fieldStart:i]
	if field == "" {
		return nil, &ParseError{Column: col, Message: "Expected a field name"}
	}
	// Проверяем whitelist
	allowed := false
	for _, f := range allowedFields {
		if f == field {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, &ParseError{Column: col, Message: fmt.Sprintf("Unknown field: %q", field)}
	}

	// 2. Пропускаем (опциональные) пробелы
	for i < len(input) && input[i] == ' ' {
		i++
	}

	// 3. Оператор: единственный поддерживаемый сейчас — `=`
	if i >= len(input) || input[i] != '=' {
		return nil, &ParseError{Column: i + 1, Message: "Expected an operator"}
	}
	i++

	// 4. Опциональные пробелы
	for i < len(input) && input[i] == ' ' {
		i++
	}

	// 5. Значение: должно быть в "..." (double-quoted string)
	if i >= len(input) || input[i] != '"' {
		return nil, &ParseError{Column: i + 1, Message: "Expected a string, integer, date-time or boolean value"}
	}
	i++ // открывающая "
	valStart := i
	for i < len(input) && input[i] != '"' {
		i++
	}
	if i >= len(input) {
		return nil, &ParseError{Column: i + 1, Message: "Expected closing quote"}
	}
	value := input[valStart:i]
	i++ // закрывающая "

	// 6. Хвост — должен быть пустой
	for i < len(input) && input[i] == ' ' {
		i++
	}
	if i < len(input) {
		return nil, &ParseError{Column: i + 1, Message: "Unexpected token"}
	}

	return &FilterAST{Field: field, Op: "=", Value: value}, nil
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isAlphaNum(b byte) bool {
	return isLetter(b) || (b >= '0' && b <= '9')
}

// ToSQL превращает AST в безопасный SQL fragment.
// Возвращает (whereFragment, args). whereFragment использует placeholder $N,
// где N стартует с argStartIdx.
//
// Например: ast{Field:"name",Op:"=",Value:"foo"}, argStartIdx=3
//
//	→ ("name = $3", []any{"foo"}, nil)
func (a *FilterAST) ToSQL(argStartIdx int) (string, []any) {
	// Value параметризуется ($N) — инъекция через него невозможна. Field же
	// конкатенируется в SQL, поэтому его безопасность держится на Parse-whitelist'е
	// (allowedFields). Defense-in-depth: FilterAST, собранный напрямую в обход Parse,
	// мог бы пронести инъекцию в Field. Дословно эмитим только идентификатор-safe
	// Field; иначе — защитно оборачиваем через pgx.Identifier.Sanitize (двойные
	// кавычки + экранирование), гарантируя невозможность выхода за пределы
	// идентификатора (CWE-89). Легитимные поля из Parse всегда проходят без изменений.
	field := a.Field
	if !safeFieldRe.MatchString(field) {
		field = pgx.Identifier{a.Field}.Sanitize()
	}
	return fmt.Sprintf("%s = $%d", field, argStartIdx), []any{a.Value}
}
