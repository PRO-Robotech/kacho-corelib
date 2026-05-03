package operations

import (
	"context"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// Run выполняет fn в goroutine.
// При успехе → repo.MarkDone(response).
// При ошибке → repo.MarkError(status.Convert(err)).
//
// КРИТИЧНО: input ctx — это request-context handler-а, который cancel-ится
// сразу после возврата handler-ом ответа клиенту. Использовать его в worker-е
// нельзя — все cross-service gRPC-вызовы внутри fn упадут с "context canceled".
// Поэтому worker запускается с **detached** context.Background(), не наследуя
// deadline / cancel из request.
//
// Если в будущем потребуется trace-propagation — извлекать конкретные values
// (e.g. request-id) из request-ctx и переносить в bg-ctx через context.WithValue.
func Run(ctx context.Context, repo Repo, opID string, fn func(context.Context) (*anypb.Any, error)) {
	_ = ctx // intentionally unused — см. примечание выше
	go func() {
		bgCtx := context.Background()
		resp, err := fn(bgCtx)
		if err != nil {
			// Конвертируем любую ошибку в google.rpc.Status.
			st := status.Convert(err)
			_ = repo.MarkError(bgCtx, opID, st.Proto())
			return
		}
		_ = repo.MarkDone(bgCtx, opID, resp)
	}()
}
