package operations

import (
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// Principal — кто инициировал операцию. Заполняется auth-interceptor'ом
// api-gateway (E2). На E0/без auth — stub {Type: "system", ID: "bootstrap",
// DisplayName: "System"}.
//
// Семантика полей (см. sub-phase-2.0-iam-E0-skeleton-acceptance.md §14.1):
//   - Type: "user" | "service_account" | "system"
//   - ID:   "usr-..." | "sva-..." | "bootstrap"
//   - DisplayName: human-readable email / SA-name / "System"
//
// Каждая запись в operations-таблице хранит эти поля в колонках
// principal_type / principal_id / principal_display_name (миграция
// migrations/common/0002_operations_principal.sql).
type Principal struct {
	Type        string
	ID          string
	DisplayName string
}

// SystemPrincipal — stub principal для control-plane bootstrap'а / системных
// операций (миграции, фоновый retry, тесты без auth). Также используется как
// дефолт в CreateWithPrincipal-обёртке legacy-Create и в PrincipalFromContext
// для пустого ctx.
func SystemPrincipal() Principal {
	return Principal{Type: "system", ID: "bootstrap", DisplayName: "System"}
}

// Operation — domain-тип, зеркалит proto Operation.
// Используется repo / service-слоями внутри Kachō-сервисов.
type Operation struct {
	ID          string
	Description string
	CreatedAt   time.Time
	CreatedBy   string
	ModifiedAt  time.Time
	Done        bool
	Metadata    *anypb.Any     // специфичные метаданные (CreateInstanceMetadata и т.д.)
	Error       *status.Status // заполнен если done && ошибка
	Response    *anypb.Any     // заполнен если done && успех — финальное состояние ресурса

	// Principal — кто инициировал операцию (kacho-iam-resolved). На E0/без
	// auth заполняется SystemPrincipal(). На E2+ заполняется из auth-ctx
	// через PrincipalFromContext.
	Principal Principal
}
