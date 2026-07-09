// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package authz — listobjects.go.
//
// Реализация FGA-filtered List filtering для backend-сервисов Kachō:
// вместо клиентского "fetch-all → filter-after" pattern (TOCTOU + leak в empty
// list) — единый ListAllowedIDs(p, objectType, relation) → set of allowed ids,
// который сервис подставляет в SQL `WHERE id = ANY($1::text[])`.
//
// # Архитектура
//
//	┌──────────────┐                          ┌──────────────────────────┐
//	│   handler    │ ─ ListAllowedIDs(p,t,r) ─►│  authz.ListObjectsService│
//	│  (List RPC)  │                          │  (per-service)           │
//	└──────────────┘                          │                          │
//	                                          │  1. cacheKey(p,t,r,m)    │
//	                                          │  2. cache.Get (≤0.5ms)   │
//	                                          │  3. miss → client call   │
//	                                          │  4. cache.Put (TTL=5s)   │
//	                                          │  5. return ids           │
//	                                          └──────────────────────────┘
//	                                                  │
//	                                                  ▼ ListObjects(subject,
//	                                                               resource_type,
//	                                                               action)
//	                                          ┌──────────────────────────┐
//	                                          │  kacho-iam :9090         │
//	                                          │  AuthorizeService        │
//	                                          │    .ListObjects          │
//	                                          └──────────────────────────┘
//	                                                  │
//	                                                  ▼
//	                                          ┌──────────────────────────┐
//	                                          │  OpenFGA (ListObjects)   │
//	                                          └──────────────────────────┘
//
// # Cache invalidation
//
//   - Cache TTL = 5s positive-only.
//   - Push-invalidation через тот же channel `kacho_iam_subjects` (re-uses
//     existing LISTEN-invalidator из listen_invalidate.go).
//   - Worst-case: TTL=5s + NOTIFY≤1s = ≤6s.
//
// # Fail modes
//
//   - kacho-iam unreachable → ErrUnavailable (fail-closed); если fresh entry
//     в кэше — оно возвращается (graceful degradation).
//   - Если KACHO_AUTHZ_LISTOBJECTS_FAIL_OPEN=true → caller инструктирован
//     fallback'нуть на unfiltered List (degraded mode; WARN-log + Critical-alert).
//   - Empty grant (len(ids)==0) → caller возвращает empty Response (HTTP 200),
//     НЕ PermissionDenied.
//
// # Decoupling от kacho-proto
//
// Пакет НЕ импортирует kacho-proto stubs (как и весь corelib/authz, см. doc.go).
// Определяет узкий port-интерфейс ListObjectsClient. Реализация —
// `<service>/internal/clients/iam_listobjects_client.go` поверх iamv1.AuthorizeServiceClient.
package authz

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListObjectsClient — port-интерфейс для kacho-iam AuthorizeService.ListObjects.
// Реализация — adapter в каждом сервисе (decoupling corelib от kacho-proto).
//
// Семантика:
//   - subjectID: FGA-style subject "user:usr_xxx" / "service_account:sva_xxx".
//   - resourceType: FGA object type, например "vpc_network".
//   - action: domain.resource.verb, например "vpc.networks.read".
//   - maxResults: hard cap (≤10000). 0 → server default.
//   - pageToken: для pagination больших списков.
//
// Возвращает: bare ids (без "<type>:" префикса), nextPageToken (empty == done).
// err != nil → fail-closed.
type ListObjectsClient interface {
	ListObjects(ctx context.Context, req ListObjectsRequest) (ListObjectsResponse, error)
}

// ListObjectsRequest — параметры одного ListObjects-вызова.
type ListObjectsRequest struct {
	Subject      string
	ResourceType string
	Action       string
	MaxResults   uint32
	PageToken    string
	// AuthzModelID — pinned authorization_model_id.
	// Empty → server default. Передается через ctx-metadata (см. impl).
	AuthzModelID string
}

// ListObjectsResponse — результат FGA ListObjects.
type ListObjectsResponse struct {
	ResourceIDs   []string
	NextPageToken string
	Truncated     bool
}

// ListObjectsClientFunc — adapter, позволяющий использовать функцию как ListObjectsClient
// (для тестов).
type ListObjectsClientFunc func(ctx context.Context, req ListObjectsRequest) (ListObjectsResponse, error)

// ListObjects satisfies ListObjectsClient.
func (f ListObjectsClientFunc) ListObjects(ctx context.Context, req ListObjectsRequest) (ListObjectsResponse, error) {
	return f(ctx, req)
}

// ListObjectsConfig — конфигурация ListObjectsService.
type ListObjectsConfig struct {
	// TTL — TTL положительных entry в кэше (default 5s).
	TTL time.Duration

	// MaxEntries — hard-cap на размер кэша (LRU eviction). Default 10000.
	MaxEntries int

	// MaxResults — default max_results, если caller не указал в opts.
	// Default 10000.
	MaxResults uint32

	// FollowupTimeout — таймаут одного RPC-вызова к ListObjectsClient
	// (default 500ms). Acceptance: per-RPC list latency budget ≤100ms p95
	// cache miss + roundtrip + FGA evaluation.
	FollowupTimeout time.Duration

	// AuthzModelID — pinned FGA authorization_model_id.
	// Передается на каждый Request.
	AuthzModelID string

	// ServiceName — для метрик/логов (например "kacho-vpc").
	ServiceName string
}

// ListObjectsService — оркестратор cache + client. Thread-safe.
type ListObjectsService struct {
	client ListObjectsClient
	cfg    ListObjectsConfig
	cache  *listObjectsCache
}

// NewListObjectsService собирает сервис из ListObjectsClient и config.
// client == nil → service все равно создается, но любой ListAllowedIDs вернет
// ErrUnavailable (fail-closed; используется, когда IAM endpoint не сконфигурирован).
func NewListObjectsService(client ListObjectsClient, cfg ListObjectsConfig) *ListObjectsService {
	if cfg.TTL <= 0 {
		cfg.TTL = 5 * time.Second
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 10000
	}
	if cfg.MaxResults == 0 {
		cfg.MaxResults = 10000
	}
	if cfg.FollowupTimeout <= 0 {
		cfg.FollowupTimeout = 500 * time.Millisecond
	}
	return &ListObjectsService{
		client: client,
		cfg:    cfg,
		cache:  newListObjectsCache(cfg.MaxEntries, cfg.TTL),
	}
}

// ListAllowedIDsOptions — per-call overrides.
type ListAllowedIDsOptions struct {
	// MaxResults — override-cap. 0 → используется cfg.MaxResults.
	MaxResults uint32

	// ScopeHint — опциональный scope для cache key separation (например project_id);
	// разные scopes → разные cache entries.
	ScopeHint string

	// AuthzModelID — override pinned model id. 0 → cfg.AuthzModelID.
	AuthzModelID string

	// SkipCache — если true, bypass cache (для post-write read-your-own-writes).
	SkipCache bool
}

// ListAllowedIDs — основной метод. Возвращает все ids ресурсов типа resourceType,
// на которые subject имеет relation action.
//
// Семантика:
//   - subjectID/resourceType/action == "" → validation error (обязательные
//     аргументы; это caller-баг, НЕ fail-closed ErrUnavailable — caller обязан
//     выставить principal перед listing'ом; анонимного listing'а с FGA нет).
//   - cache hit → возвращает cached (≤1ms p95).
//   - cache miss → client.ListObjects + cache.Put + return.
//   - client error → fail-closed: ErrUnavailable.
//   - len(ids) == 0 → возвращает ([], nil) — caller вернет empty response.
func (s *ListObjectsService) ListAllowedIDs(
	ctx context.Context,
	subjectID, resourceType, action string,
	opts ListAllowedIDsOptions,
) ([]string, error) {
	if s == nil || s.client == nil {
		return nil, ErrUnavailable
	}
	if subjectID == "" || resourceType == "" || action == "" {
		return nil, fmt.Errorf("authz.ListAllowedIDs: subject/resourceType/action required")
	}

	modelID := opts.AuthzModelID
	if modelID == "" {
		modelID = s.cfg.AuthzModelID
	}
	maxResults := opts.MaxResults
	if maxResults == 0 {
		maxResults = s.cfg.MaxResults
	}

	key := cacheKeyFor(subjectID, resourceType, action, opts.ScopeHint, modelID)
	if !opts.SkipCache {
		if ids, ok := s.cache.get(key); ok {
			return ids, nil
		}
	}

	// Cache miss: каждый page-вызов client под собственным FollowupTimeout
	// (per-call deadline на КАЖДОМ внешнем вызове — architecture.md). Единый
	// таймаут на весь paginating-loop спуриозно резал бы здоровый multi-page peer:
	// 2-я страница унаследовала бы усохший остаток бюджета и упала бы closed в
	// ErrUnavailable, хотя peer жив и просто paginating. Поле FollowupTimeout
	// документировано как «таймаут одного RPC-вызова» — применяем per-page.
	// Defense-in-depth: bound the paginating loop. Each page carries its own
	// FollowupTimeout (per-call deadline), but there is otherwise no aggregate
	// limit over the whole loop — only the parent ctx. A buggy/adversarial
	// ListObjects returning a perpetual non-empty NextPageToken whose pages never
	// accumulate to maxResults (repeated empty pages, or an unchanged token)
	// would spin this handler goroutine forever and hammer FGA. Two guards bound
	// it without cutting a healthy multi-page peer (which either fills maxResults
	// or exhausts its token first): (a) an absolute page cap — we never need more
	// than maxResults pages to accumulate maxResults ids; (b) an unchanged-token
	// break — an honest paginator never hands back the token it was just given.
	maxPages := int(maxResults)
	if maxPages < 1 {
		maxPages = 1
	}
	allIDs := make([]string, 0, 64)
	pageToken := ""
	for pages := 0; ; pages++ {
		resp, err := s.listObjectsOnce(ctx, ListObjectsRequest{
			Subject:      subjectID,
			ResourceType: resourceType,
			Action:       action,
			MaxResults:   maxResults,
			PageToken:    pageToken,
			AuthzModelID: modelID,
		})
		if err != nil {
			// Разделяем PermissionDenied (легитимный denial → 403)
			// от Unavailable (infra недоступна → 503). До этого fix'а оба
			// сваливались в ErrUnavailable, и стенд возвращал UI 503 на cases
			// где FGA model просто не имела пути для subject (что должно быть
			// 403 — "у тебя нет прав", а не "сервис сломан").
			if status.Code(err) == codes.PermissionDenied {
				return nil, fmt.Errorf("%w: %v", ErrPermissionDenied, err)
			}
			// Fail-closed default: все остальное (timeout, connection-refused, …) → Unavailable.
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		allIDs = append(allIDs, resp.ResourceIDs...)
		if resp.NextPageToken == "" {
			break
		}
		if uint32(len(allIDs)) >= maxResults {
			break
		}
		// Fail-safe (not forever): stuck token, or more pages than we could ever
		// need to fill maxResults → stop. Under-returning is correctness-neutral
		// here: the next cache-miss re-fetches authoritatively.
		if resp.NextPageToken == pageToken || pages+1 >= maxPages {
			break
		}
		pageToken = resp.NextPageToken
	}

	// Cache result (даже empty — empty grant нужно кешировать чтобы не звать
	// FGA на каждый poll). InvalidateBySubject выкинет это на revoke.
	s.cache.put(key, subjectID, allIDs)
	return allIDs, nil
}

// listObjectsOnce делает ровно один ListObjects-вызов под собственным
// per-call deadline (FollowupTimeout). Выделен, чтобы `defer cancel()` каждой
// страницы срабатывал по завершении именно этой итерации, а не в конце всего
// paginating-loop'а.
func (s *ListObjectsService) listObjectsOnce(ctx context.Context, req ListObjectsRequest) (ListObjectsResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.FollowupTimeout)
	defer cancel()
	return s.client.ListObjects(callCtx, req)
}

// InvalidateBySubject — вызывается из LISTEN-invalidator при NOTIFY
// kacho_iam_subjects (re-uses пакет-level Cache мехнизма из listen_invalidate.go).
func (s *ListObjectsService) InvalidateBySubject(subjectID string) {
	if s == nil || s.cache == nil {
		return
	}
	s.cache.invalidateBySubject(subjectID)
}

// InvalidateAll — periodic / reconnect safety net.
func (s *ListObjectsService) InvalidateAll() {
	if s == nil || s.cache == nil {
		return
	}
	s.cache.invalidateAll()
}

// Size — (subjects, entries) для метрик.
func (s *ListObjectsService) Size() (subjects, entries int) {
	if s == nil || s.cache == nil {
		return 0, 0
	}
	return s.cache.size()
}

// cacheKeyFor собирает ключ в детерминированном формате:
//
//	"<subject>|<resource_type>|<action>|<scope>|<model_id>"
//
// Principal type/id ужé encoded в subject string FGA-style
// ("user:usr_alice"), поэтому отдельные поля не нужны.
func cacheKeyFor(subject, resourceType, action, scope, modelID string) string {
	return subject + "|" + resourceType + "|" + action + "|" + scope + "|" + modelID
}

// listObjectsCache — простой two-level cache:
//
//	subject_id → map[cacheKey]entry
//
// Двухуровневая структура дает O(1) invalidateBySubject (one delete on
// outer map).
//
// LRU eviction — вторичная функция (защита от memory leak); основной механизм
// — TTL.
type listObjectsCache struct {
	mu      sync.RWMutex
	maxSize int
	ttl     time.Duration
	now     func() time.Time

	// store: subject → set of entries
	store map[string]map[string]listObjectsEntry

	// count — точное текущее число entry во всех subject-bucket'ах, зеркалит
	// authz.Cache.count. Поддерживается инкрементально на insert/delete, чтобы
	// size-check в evictIfNeededLocked был O(1), а не full map-traversal на
	// КАЖДОМ put() (иначе даже без единой эвикции N put'ов стоили бы O(N^2)).
	count int
}

type listObjectsEntry struct {
	ids       []string
	expiresAt time.Time
}

func newListObjectsCache(maxSize int, ttl time.Duration) *listObjectsCache {
	if maxSize <= 0 {
		maxSize = 10000
	}
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &listObjectsCache{
		maxSize: maxSize,
		ttl:     ttl,
		now:     time.Now,
		store:   make(map[string]map[string]listObjectsEntry, 64),
	}
}

// setNowFunc подменяет источник времени (для детерминированных TTL-тестов),
// зеркалит Cache.SetNowFunc. Prod-путь использует time.Now (см. newListObjectsCache).
func (c *listObjectsCache) setNowFunc(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// extractSubjectFromKey — first field в "subject|...".
func extractSubjectFromKey(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[:i]
		}
	}
	return key
}

func (c *listObjectsCache) get(key string) ([]string, bool) {
	c.mu.RLock()
	subject := extractSubjectFromKey(key)
	subMap, ok := c.store[subject]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}
	e, ok := subMap[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if c.now().After(e.expiresAt) {
		c.mu.Lock()
		if subMap, ok := c.store[subject]; ok {
			if _, stillPresent := subMap[key]; stillPresent {
				delete(subMap, key)
				c.count--
				if len(subMap) == 0 {
					delete(c.store, subject)
				}
			}
		}
		c.mu.Unlock()
		return nil, false
	}
	// Defensive copy — caller doesn't get internal slice.
	out := make([]string, len(e.ids))
	copy(out, e.ids)
	return out, true
}

func (c *listObjectsCache) put(key, subjectID string, ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Defensive copy.
	stored := make([]string, len(ids))
	copy(stored, ids)
	newEntry := listObjectsEntry{
		ids:       stored,
		expiresAt: c.now().Add(c.ttl),
	}

	// Overwrite-in-place (renewal of an existing key) doesn't change size —
	// no eviction check needed. Зеркалит Cache.SetAllowed.
	if sm, ok := c.store[subjectID]; ok {
		if _, exists := sm[key]; exists {
			sm[key] = newEntry
			return
		}
	}

	// New key — bound cache size BEFORE insert (O(1) count check, see
	// evictIfNeededLocked doc).
	if c.count >= c.maxSize {
		c.evictIfNeededLocked()
	}
	subMap, ok := c.store[subjectID]
	if !ok {
		subMap = make(map[string]listObjectsEntry, 4)
		c.store[subjectID] = subMap
	}
	subMap[key] = newEntry
	c.count++
}

// evictIfNeededLocked приводит размер кеша под maxSize. Вызывается под write
// lock из put() ПЕРЕД вставкой нового ключа, когда count достиг maxSize.
// Зеркалит authz.Cache.evictLocked (cache.go): фаза 1 — дешёвый
// expired-sweep (точнее и почти всегда достаточно — просроченные entries
// обычно и есть тот "излишек"); фаза 2 (если всё ещё полно) — произвольная
// map-iteration эвикция до low-water (maxSize - maxSize/10), БЕЗ сортировки.
//
// Раньше (round-7 audit finding) эта функция (a) на каждом put() пересчитывала
// total полным обходом ВСЕГО store (O(N) даже когда эвикция не нужна — O(N^2)
// суммарно на N put'ов) и (b) при переполнении делала insertion-sort по ВСЕМ
// live entries (O(N^2) на саму эвикцию, N~10000 → ~2.5e7 swaps под write
// lock, блокируя все concurrent get()). Оба обхода заменены: (a) — на
// инкрементальный c.count (обновляется на каждой вставке/удалении), (b) — на
// два O(N)-прохода по map без сортировки. Произвольная эвикция
// correctness-neutral: cache-miss всегда откатывается на авторитетный
// ListObjects-вызов (fail-closed re-fetch) — эвикция бьёт только по
// hit-rate, не по корректности.
func (c *listObjectsCache) evictIfNeededLocked() {
	if c.count < c.maxSize {
		return
	}

	// Фаза 1: expired-sweep.
	now := c.now()
	for subj, sm := range c.store {
		for k, e := range sm {
			if now.After(e.expiresAt) {
				delete(sm, k)
				c.count--
			}
		}
		if len(sm) == 0 {
			delete(c.store, subj)
		}
	}
	if c.count < c.maxSize {
		return
	}

	// Фаза 2: всё ещё полно — эвиктим произвольные entry (map-iteration
	// order) до low-water, чтобы не триггерить эвикцию на каждом следующем put.
	target := c.maxSize - c.maxSize/10
	if target < 0 {
		target = 0
	}
	for subj, sm := range c.store {
		for k := range sm {
			if c.count <= target {
				break
			}
			delete(sm, k)
			c.count--
		}
		if len(sm) == 0 {
			delete(c.store, subj)
		}
		if c.count <= target {
			break
		}
	}
}

func (c *listObjectsCache) invalidateBySubject(subjectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sm, ok := c.store[subjectID]; ok {
		c.count -= len(sm)
		delete(c.store, subjectID)
	}
}

func (c *listObjectsCache) invalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store = make(map[string]map[string]listObjectsEntry, 64)
	c.count = 0
}

func (c *listObjectsCache) size() (subjects, entries int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	subjects = len(c.store)
	entries = c.count
	return
}
