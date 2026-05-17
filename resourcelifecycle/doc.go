// Package resourcelifecycle реализует server-side helper для
// `InternalResourceLifecycleService.Subscribe` server-stream'а (D-13;
// acceptance §4.7).
//
// Используется в kacho-vpc / kacho-compute / kacho-loadbalancer для drain'а
// per-service outbox-table'ы lifecycle-событий и push'а через gRPC server
// stream к kacho-iam consumer'у (resource_lifecycle_subscriber worker).
//
// # Контракт
//
//	type Event struct {
//	    EventID          string // monotonic из outbox.sequence_no
//	    Kind             string // "network" | "subnet" | "instance" | ...
//	    ResourceID       string
//	    Op               string // "Create" | "Delete" | "Move"
//	    ProjectID        string
//	    ParentResourceID string // empty если parent = project
//	    OldProjectID     string // для op=Move
//	    CreatedAt        time.Time
//	}
//
//	type Source interface {
//	    // ReadFrom возвращает batch событий из outbox с `event_id > cursor`,
//	    // ограниченный limit. На пустоту возвращает nil events; caller засыпает
//	    // и подписан на pg_notify channel.
//	    ReadFrom(ctx context.Context, kinds []string, cursor string, limit int) ([]Event, error)
//	    // WaitForChange ждёт LISTEN-notify или timeout. На notify — wake-up.
//	    WaitForChange(ctx context.Context, timeout time.Duration) error
//	}
//
//	type Sink interface {
//	    Send(Event) error
//	}
//
//	func Stream(ctx, src, sink, kinds, cursor) error — основной server-loop.
//
// # D-13 rationale
//
// Этот пакет НЕ заменяет per-service outbox / LISTEN/NOTIFY инфраструктуру
// (`vpc_outbox`, kacho-vpc internal_watch_handler.go) — только обёртку под
// специализированный server-stream для FGA-sync. Existing InternalWatchService
// был general-purpose (controllers reconciliation); InternalResourceLifecycle
// специфичен (только Create/Delete/Move-tuples). Coexist пока полная миграция
// kacho-vpc-implement не закроет необходимость WatchService.
package resourcelifecycle
