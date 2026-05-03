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
// Пример использования в service.CreateInstance:
//
//	op, _ := operations.New("Create instance "+name, &CreateInstanceMetadata{InstanceId: uid})
//	_ = repo.Create(ctx, op)
//	operations.Run(ctx, repo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
//	    inst := buildInstance(...)
//	    return anypb.New(inst)
//	})
//	return op
//
// ctx передаётся в fn — позволяет передать deadline / trace span.
// ctx НЕ отменяет goroutine при отмене; fn сам обязан проверять ctx.Done().
func Run(ctx context.Context, repo Repo, opID string, fn func(context.Context) (*anypb.Any, error)) {
	go func() {
		resp, err := fn(ctx)
		if err != nil {
			// Конвертируем любую ошибку в google.rpc.Status.
			st := status.Convert(err)
			_ = repo.MarkError(ctx, opID, st.Proto())
			return
		}
		_ = repo.MarkDone(ctx, opID, resp)
	}()
}
