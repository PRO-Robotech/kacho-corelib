package authz

import (
	"context"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// defaultSubjectExtractor — стандартная реализация на основе
// operations.PrincipalFromContext (E2-corelib).
//
// Возвращает:
//   - subjectFGA — "user:usr_xxx" или "service_account:sva_xxx"
//   - principalID — raw ID (для rate-limit-bucket'а)
//   - ok — false если ctx пустой / нет Principal'а
//
// На system-principal (без override AllowSystemPrincipal=true) — возвращает
// ok=true, subjectFGA="user:bootstrap". Interceptor.authorize дальше
// reject'нет (subject "user:bootstrap" не имеет tuples в FGA).
func defaultSubjectExtractor(ctx context.Context) (string, string, bool) {
	p := operations.PrincipalFromContext(ctx)
	if p.ID == "" {
		return "", "", false
	}
	return FormatSubject(p.Type, p.ID), p.ID, true
}
