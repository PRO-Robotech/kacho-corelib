// Package outbox реализует транзакционный outbox-паттерн для Kachō Watch infrastructure.
// Каждое мутирующее действие на ресурс пишет событие в resource_events в той же транзакции.
package outbox

// Event описывает одно событие изменения ресурса.
type Event struct {
	// EventType — тип события: "ADDED", "MODIFIED", "DELETED".
	EventType string
	// ResourceKind — тип ресурса, например "Organization", "Cloud", "Folder".
	ResourceKind string
	// ResourceUID — UUID ресурса в виде строки.
	ResourceUID string
	// Data — protobuf-сериализованный ресурс (nil допускается для DELETED-tombstone).
	Data []byte
}
