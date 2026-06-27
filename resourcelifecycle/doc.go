// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package resourcelifecycle реализует server-side helper для
// `InternalResourceLifecycleService.Subscribe` server-stream'а.
//
// Helper драйнит per-service outbox-table'у lifecycle-событий и push'ит их
// через gRPC server-stream к consumer'у (resource_lifecycle_subscriber worker)
// для FGA-синхронизации owner-таплов.
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
//	    // WaitForChange ждет LISTEN-notify или timeout. На notify — wake-up.
//	    WaitForChange(ctx context.Context, timeout time.Duration) error
//	}
//
//	type Sink interface {
//	    Send(Event) error
//	}
//
//	func Stream(ctx, src, sink, kinds, cursor) error — основной server-loop.
//
// # Назначение
//
// Пакет не заменяет per-service outbox / LISTEN/NOTIFY инфраструктуру — это
// обертка под специализированный server-stream для FGA-синхронизации.
// InternalResourceLifecycle узко специфичен: переносит только
// Create/Delete/Move-tuples lifecycle-событий.
package resourcelifecycle
