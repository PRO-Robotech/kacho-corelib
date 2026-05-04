package operations

import (
	"fmt"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// New создаёт Operation с YC-style 20-char id, текущим временем, done=false.
// domainPrefix — 3-символьный префикс из ids.PrefixOperationRM /
// ids.PrefixOperationVPC, по которому api-gateway opsproxy маршрутизирует
// Operation.Get/Cancel в нужный backend.
//
// Если prefix пуст — id без prefix (legacy/internal use); такая операция не
// маршрутизируется через opsproxy и доступна только локально внутри сервиса.
//
// metadata — proto-сообщение специфичное для типа RPC
// (например, CreateInstanceMetadata{instance_id: uid}).
func New(domainPrefix, description string, metadata proto.Message) (Operation, error) {
	var anyMeta *anypb.Any
	if metadata != nil {
		var err error
		anyMeta, err = anypb.New(metadata)
		if err != nil {
			return Operation{}, fmt.Errorf("operations.New: marshal metadata: %w", err)
		}
	}

	var id string
	if domainPrefix == "" {
		// legacy без префикса: используем NewUID (rev-sentinel id).
		id = ids.NewUID()
	} else {
		id = ids.NewID(domainPrefix)
	}

	now := time.Now().UTC()
	return Operation{
		ID:          id,
		Description: description,
		CreatedAt:   now,
		CreatedBy:   "anonymous",
		ModifiedAt:  now,
		Done:        false,
		Metadata:    anyMeta,
	}, nil
}

// MetadataFor извлекает типизированные метаданные из операции.
// Возвращает ошибку, если Metadata nil или тип не совпадает.
func MetadataFor[T proto.Message](op *Operation) (T, error) {
	var zero T
	if op.Metadata == nil {
		return zero, fmt.Errorf("operations.MetadataFor: metadata is nil")
	}
	msg, err := op.Metadata.UnmarshalNew()
	if err != nil {
		return zero, fmt.Errorf("operations.MetadataFor: unmarshal: %w", err)
	}
	typed, ok := msg.(T)
	if !ok {
		return zero, fmt.Errorf("operations.MetadataFor: unexpected type %T", msg)
	}
	return typed, nil
}
