// Package validate содержит общие валидаторы полей API Kachō, общие для всех
// сервисов (Folder.Name, Network.Name, Subnet.Name и т. п.).
//
// Все валидаторы возвращают gRPC ошибку `InvalidArgument` с
// `BadRequest.field_violations[]` через `kacho-corelib/errors.InvalidArgument()`.
//
// Контракт verbatim от YC:
//   - Name: 2..63 символов, regex `^[a-z][-a-z0-9]{0,61}[a-z0-9]$` (короткое
//     имя из строчных букв, цифр и дефисов; начинается с буквы; не оканчивается
//     дефисом). Пустое имя — отдельная проверка `name is required`.
//   - Description: до 256 символов.
//   - Labels: до 64 пар; ключ `^[a-z][-_./\\a-z0-9]{0,62}$` (1..63 байта);
//     значение 0..63 байта.
//   - UpdateMask: каждое поле должно быть известно сервисом; неизвестное —
//     `InvalidArgument`.
package validate

import (
	"regexp"
	"unicode/utf8"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
)

// nameRe — verbatim YC contract name regex.
//
// Ровно: первый символ — строчная буква; далее — буквы, цифры, дефис; последний
// символ — буква или цифра (не дефис). Длина 2..63.
var nameRe = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)

// labelKeyRe — YC label key regex (строчные + цифры + `-_./\`).
var labelKeyRe = regexp.MustCompile(`^[a-z][-_./\\a-z0-9]{0,62}$`)

const (
	// MaxNameLen — максимум для Name полей ресурсов (verbatim YC).
	MaxNameLen = 63
	// MaxDescriptionLen — лимит описания.
	MaxDescriptionLen = 256
	// MaxLabels — максимальное число label-пар на ресурс.
	MaxLabels = 64
	// MaxLabelKeyLen — длина ключа label.
	MaxLabelKeyLen = 63
	// MaxLabelValueLen — длина значения label.
	MaxLabelValueLen = 63
	// MaxPageSize — верхняя граница для page_size в List RPC (verbatim YC).
	MaxPageSize int64 = 1000
	// DefaultPageSize — значение по-умолчанию, когда клиент не задал page_size.
	DefaultPageSize int64 = 50
)

// Name проверяет, что value соответствует verbatim YC name-контракту.
//
// Возвращает err типа InvalidArgument с FieldViolation, либо nil если ok.
// Не проверяет «is required» — это делает caller отдельной проверкой
// `value == ""`, чтобы сообщение было понятным.
func Name(field, value string) error {
	if !nameRe.MatchString(value) {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+` must match ^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$ (lowercase letters, digits, hyphens; starts with letter, ends with letter or digit; 2..63 chars)`).
			Err()
	}
	return nil
}

// Description проверяет длину поля description (UTF-8).
func Description(field, value string) error {
	if utf8.RuneCountInString(value) > MaxDescriptionLen {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" length exceeds 256 chars").
			Err()
	}
	return nil
}

// Labels проверяет map labels: число пар, длину и regex ключа, длину значения.
func Labels(field string, labels map[string]string) error {
	if len(labels) > MaxLabels {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, "too many labels (max 64)").
			Err()
	}
	for k, v := range labels {
		if len(k) == 0 || len(k) > MaxLabelKeyLen || !labelKeyRe.MatchString(k) {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field+"."+k, "invalid label key (1..63 chars, lowercase letters, digits, _-./\\)").
				Err()
		}
		if len(v) > MaxLabelValueLen {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field+"."+k, "label value exceeds 63 chars").
				Err()
		}
	}
	return nil
}

// PageSize проверяет границы page_size в List RPC.
//
// Семантика — verbatim YC contract:
//   - page_size == 0 → допустимо; клиент явно не задал, репозиторий применяет
//     DefaultPageSize. Возвращает (DefaultPageSize, nil).
//   - page_size < 0 или > MaxPageSize → InvalidArgument с FieldViolation;
//     возвращает (0, err). Не silent fallback — это нарушение контракта.
//   - 1..MaxPageSize → возвращает (value, nil).
//
// Возвращаемое effective значение нужно использовать в LIMIT-выражении SQL.
// Каждый репозиторий-метод List должен вызывать PageSize первой строкой
// и пробрасывать err наружу через service.
func PageSize(field string, value int64) (int64, error) {
	if value < 0 || value > MaxPageSize {
		return 0, coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" must be in [0..1000] (0 means default)").
			Err()
	}
	if value == 0 {
		return DefaultPageSize, nil
	}
	return value, nil
}

// UpdateMask проверяет, что все field-ы в mask содержатся в known.
//
// Используется в *.Update методах: каждый сервис указывает свой набор
// разрешённых для апдейта полей; всё остальное — InvalidArgument.
func UpdateMask(field string, mask []string, known map[string]struct{}) error {
	for _, f := range mask {
		if _, ok := known[f]; !ok {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field, "unknown field in update_mask: "+f).
				Err()
		}
	}
	return nil
}
