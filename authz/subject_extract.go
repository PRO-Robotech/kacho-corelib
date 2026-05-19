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

// isAnonymousSubject — KAC-122 helper. Returns true для всех принципалов
// эквивалентных anonymous (closed-list match):
//
//   - empty subject / empty principal_id
//   - principal_id == "anonymous" (api-gateway injectAnonymous case)
//   - subject == "system:anonymous"
//   - principal_id == "bootstrap" / subject == "system:bootstrap"
//     (PrincipalFromContext fallback когда ctx без Principal — api-gateway
//     не передал x-kacho-principal-* metadata headers).
//
// Используется в breakglass-path: даже когда Keto-Check недоступен,
// anonymous request'ы должны быть denied (CRIT-6/7 fix).
func isAnonymousSubject(extract func(context.Context) (string, string, bool), ctx context.Context) bool {
	subjectFGA, principalID, ok := extract(ctx)
	if !ok || principalID == "" || subjectFGA == "" {
		return true
	}
	if principalID == "anonymous" || principalID == "bootstrap" {
		return true
	}
	if subjectFGA == "system:anonymous" || subjectFGA == "system:bootstrap" {
		return true
	}
	return false
}
