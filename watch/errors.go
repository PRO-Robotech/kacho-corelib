package watch

import "errors"

// ErrGone возвращается из Subscribe, когда запрошенный resourceVersion
// устарел (меньше минимального в outbox после retention cleanup).
// Клиент должен выполнить List для получения текущего resourceVersion
// и начать новый Watch от него (Gone 410, gRPC OUT_OF_RANGE).
var ErrGone = errors.New("Gone: resourceVersion too old, please relist")
