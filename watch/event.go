// Package watch реализует in-process Watch Hub для Kachō Watch infrastructure.
// Hub читает события из таблицы resource_events через polling + LISTEN/NOTIFY,
// ведёт ring buffer последних 1024 событий и fan-out на подписчиков.
package watch

// Event описывает одно событие изменения ресурса из outbox.
type Event struct {
	// ResourceVersion — монотонно возрастающий номер версии из sequence.
	ResourceVersion int64
	// EventType — тип события: "ADDED", "MODIFIED", "DELETED".
	EventType string
	// ResourceKind — тип ресурса, например "Organization".
	ResourceKind string
	// ResourceUID — UUID ресурса.
	ResourceUID string
	// Data — protobuf-сериализованный ресурс (nil для DELETED-tombstone).
	Data []byte
}
