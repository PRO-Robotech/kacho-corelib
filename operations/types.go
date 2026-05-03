package operations

import (
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

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
	Response    *anypb.Any    // заполнен если done && успех — финальное состояние ресурса
}
