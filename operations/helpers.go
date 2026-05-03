package operations

import (
	"fmt"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// New создаёт Operation с UUID, текущим временем, done=false.
// metadata — proto-сообщение специфичное для типа RPC
// (например, CreateInstanceMetadata{instance_id: uid}).
func New(description string, metadata proto.Message) (Operation, error) {
	var anyMeta *anypb.Any
	if metadata != nil {
		var err error
		anyMeta, err = anypb.New(metadata)
		if err != nil {
			return Operation{}, fmt.Errorf("operations.New: marshal metadata: %w", err)
		}
	}

	now := time.Now().UTC()
	return Operation{
		ID:          ids.NewUID(),
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
