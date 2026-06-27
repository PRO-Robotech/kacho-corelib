// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

var metaTestFileSeq atomic.Int64

// buildMetadata строит синтетическое proto-сообщение с указанными string-полями
// по их exact-именам (через dynamicpb), чтобы протестировать рефлексию
// extractResourceID / extractAccountID без зависимости от kacho-iam proto-stubs.
// fields — упорядоченный список (имя, значение); порядок задает field-number 1..N,
// что важно для проверки «account_id — non-first поле, первое _id дает resource_id».
//
// Синтетический тип регистрируется в глобальном protoregistry, чтобы продовый
// anypb.UnmarshalNew (глобальный resolver в extractResourceID/extractAccountID)
// смог его разрезолвить после round-trip'а через operations.New → anypb.New.
func buildMetadata(t *testing.T, msgName string, fields ...[2]string) protoreflect.ProtoMessage {
	t.Helper()

	var fieldDescs []*descriptorpb.FieldDescriptorProto
	for i, f := range fields {
		num := int32(i + 1)
		name := f[0]
		fieldDescs = append(fieldDescs, &descriptorpb.FieldDescriptorProto{
			Name:   strPtr(name),
			Number: &num,
			Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		})
	}

	syntax := "proto3"
	// Уникальный package на каждый вызов — избегаем коллизий в глобальном registry.
	seq := metaTestFileSeq.Add(1)
	pkg := fmt.Sprintf("kacho.test.operations.s%d", seq)
	cleanName := sanitizeProtoIdent(msgName)
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    strPtr(fmt.Sprintf("operations_test_%s_%d.proto", cleanName, seq)),
		Package: &pkg,
		Syntax:  &syntax,
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: strPtr(cleanName), Field: fieldDescs},
		},
	}

	fd, err := protodesc.NewFile(fdp, nil)
	require.NoError(t, err)

	md := fd.Messages().Get(0)
	require.NoError(t, protoregistry.GlobalFiles.RegisterFile(fd))
	require.NoError(t, protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(md)))

	dyn := dynamicpb.NewMessage(md)
	for _, f := range fields {
		fldDesc := md.Fields().ByName(protoreflect.Name(f[0]))
		require.NotNil(t, fldDesc, "field %q должно существовать в дескрипторе", f[0])
		dyn.Set(fldDesc, protoreflect.ValueOfString(f[1]))
	}
	return dyn
}

func strPtr(s string) *string { return &s }

// sanitizeProtoIdent делает из произвольной строки валидный proto-идентификатор
// (буквы/цифры/_; первый символ — буква/_), чтобы имя тест-сообщения можно было
// собирать из произвольных id (с дефисами) без падения descriptorpb-валидации.
func sanitizeProtoIdent(s string) string {
	if s == "" {
		return "M"
	}
	out := make([]rune, 0, len(s))
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			out = append(out, r)
		case r >= '0' && r <= '9' && i > 0:
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if first := out[0]; (first < 'a' || first > 'z') && (first < 'A' || first > 'Z') && first != '_' {
		out = append([]rune{'M', '_'}, out...)
	}
	return string(out)
}

// ---- (a) op с metadata, несущим account_id → колонка account_id заполнена ----

// TestRepo_AccountID_PopulatedFromMetadata — IAM-путь: metadata с exact-полем
// account_id (non-first; первое _id поле — project_id) → resource_id=project_id,
// account_id=account_id. Проверяет: extractAccountID читает по точному имени +
// disambiguation project_id/account_id.
func TestRepo_AccountID_PopulatedFromMetadata(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := operations.NewRepo(pool, "public")

	// metadata: поле 1 = project_id (дает resource_id), поле 2 = account_id.
	meta := buildMetadata(t, "CreateProjectMetadata",
		[2]string{"project_id", "prj-AAA"},
		[2]string{"account_id", "acc-X"},
	)
	op, err := operations.New("iop", "create project", meta)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(ctx, op, operations.SystemPrincipal()))

	var resourceID, accountID *string
	err = pool.QueryRow(ctx, `SELECT resource_id, account_id FROM operations WHERE id=$1`, op.ID).
		Scan(&resourceID, &accountID)
	require.NoError(t, err)
	require.NotNil(t, resourceID)
	require.NotNil(t, accountID)
	assert.Equal(t, "prj-AAA", *resourceID, "resource_id должен быть первым _id-полем (project_id)")
	assert.Equal(t, "acc-X", *accountID, "account_id должен читаться по точному имени")
}

// TestRepo_AccountID_ExactNameNotSuffixLoop — extractAccountID НЕ путает
// project_id/user_id: metadata {user_id, account_id} → resource_id=user_id,
// account_id=account_id (а НЕ user_id).
func TestRepo_AccountID_ExactNameNotSuffixLoop(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := operations.NewRepo(pool, "public")

	// InviteUserMetadata-like: первое _id поле = user_id; account_id — non-first.
	meta := buildMetadata(t, "InviteUserMetadata",
		[2]string{"user_id", "usr-W"},
		[2]string{"account_id", "acc-X"},
		[2]string{"magic_link_url", "https://example/x"},
	)
	op, err := operations.New("iop", "invite user", meta)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(ctx, op, operations.SystemPrincipal()))

	var resourceID, accountID *string
	err = pool.QueryRow(ctx, `SELECT resource_id, account_id FROM operations WHERE id=$1`, op.ID).
		Scan(&resourceID, &accountID)
	require.NoError(t, err)
	require.NotNil(t, resourceID)
	require.NotNil(t, accountID)
	assert.Equal(t, "usr-W", *resourceID, "resource_id = первое _id (user_id), не account_id")
	assert.Equal(t, "acc-X", *accountID, "account_id по точному имени, не user_id")
}

// ---- (b) op без account_id (non-IAM) → колонка NULL, insert ok (regression) ----

// TestRepo_AccountID_NullWhenAbsent — regression-guard для vpc/compute/nlb/apps:
// metadata без поля account_id → account_id IS NULL, insert успешен,
// resource_id по-прежнему берется из первого _id-поля (additive).
func TestRepo_AccountID_NullWhenAbsent(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := operations.NewRepo(pool, "public")

	// VPC-like metadata: только subnet_id, нет account_id.
	meta := buildMetadata(t, "CreateSubnetMetadata",
		[2]string{"subnet_id", "snt-Q"},
	)
	op, err := operations.New("opv", "create subnet", meta)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(ctx, op, operations.SystemPrincipal()))

	var resourceID, accountID *string
	err = pool.QueryRow(ctx, `SELECT resource_id, account_id FROM operations WHERE id=$1`, op.ID).
		Scan(&resourceID, &accountID)
	require.NoError(t, err)
	require.NotNil(t, resourceID)
	assert.Equal(t, "snt-Q", *resourceID)
	assert.Nil(t, accountID, "account_id должен быть SQL NULL для не-IAM операций")
}

// TestRepo_AccountID_NullViaLegacyCreate — legacy Create-путь (через
// PrincipalFromContext/SystemPrincipal fallback) для не-IAM операции с nil
// metadata: account_id NULL, insert ok. Regression-guard для существующих
// вызовов opsRepo.Create без account_id.
func TestRepo_AccountID_NullViaLegacyCreate(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := operations.NewRepo(pool, "public")

	op, err := operations.New("opx", "legacy no-meta op", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	var accountID *string
	err = pool.QueryRow(ctx, `SELECT account_id FROM operations WHERE id=$1`, op.ID).Scan(&accountID)
	require.NoError(t, err)
	assert.Nil(t, accountID, "nil-metadata op → account_id NULL")
}

// ---- (c) List с ListFilter.AccountID → только matching, NULL-строки исключены ----

// TestRepo_List_FilterByAccountID — фильтр по account_id-колонке возвращает
// только строки с account_id=X; строки с account_id IS NULL и с другим account
// исключены (ListFilter.AccountID).
func TestRepo_List_FilterByAccountID(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := operations.NewRepo(pool, "public")

	// 3 op с account_id=acc-X (IAM, разных ресурсов).
	for _, rid := range []string{"prj-Y", "sva-Z", "grp-G"} {
		meta := buildMetadata(t, "M_"+rid,
			[2]string{"resource_id_marker", rid}, // первое поле (не _id-суффикс — намеренно)
			[2]string{"account_id", "acc-X"},
		)
		op, err := operations.New("iop", "op "+rid, meta)
		require.NoError(t, err)
		require.NoError(t, repo.CreateWithPrincipal(ctx, op, operations.SystemPrincipal()))
	}
	// 1 op с account_id=acc-OTHER (чужой account).
	otherMeta := buildMetadata(t, "M_other",
		[2]string{"x_marker", "z"},
		[2]string{"account_id", "acc-OTHER"},
	)
	otherOp, err := operations.New("iop", "op other", otherMeta)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(ctx, otherOp, operations.SystemPrincipal()))

	// 2 op без account_id (account_id NULL — категория II / не-IAM).
	for i := 0; i < 2; i++ {
		nullMeta := buildMetadata(t, fmt.Sprintf("M_null%d", i),
			[2]string{"service_account_id", fmt.Sprintf("sva-K%d", i)},
		)
		op, err := operations.New("iop", fmt.Sprintf("null-acct op %d", i), nullMeta)
		require.NoError(t, err)
		require.NoError(t, repo.CreateWithPrincipal(ctx, op, operations.SystemPrincipal()))
	}

	ops, nextToken, err := repo.List(ctx, operations.ListFilter{
		AccountID: "acc-X",
		PageSize:  100,
	})
	require.NoError(t, err)
	assert.Empty(t, nextToken)
	require.Len(t, ops, 3, "должны вернуться ровно 3 op c account_id=acc-X")

	descrs := map[string]bool{}
	for _, op := range ops {
		descrs[op.Description] = true
	}
	assert.True(t, descrs["op prj-Y"])
	assert.True(t, descrs["op sva-Z"])
	assert.True(t, descrs["op grp-G"])
	assert.False(t, descrs["op other"], "чужой account исключен")
}

// TestRepo_List_NoAccountFilter_Unchanged — пустой ListFilter.AccountID не меняет
// поведение: возвращаются ВСЕ строки (включая account_id NULL). Regression-guard.
func TestRepo_List_NoAccountFilter_Unchanged(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := operations.NewRepo(pool, "public")

	// 1 IAM-op (account_id set) + 2 не-IAM (account_id NULL).
	iamMeta := buildMetadata(t, "IamM",
		[2]string{"project_id", "prj-Y"},
		[2]string{"account_id", "acc-X"},
	)
	op, err := operations.New("iop", "iam op", iamMeta)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(ctx, op, operations.SystemPrincipal()))

	for i := 0; i < 2; i++ {
		op, err := operations.New("opv", fmt.Sprintf("vpc op %d", i), nil)
		require.NoError(t, err)
		require.NoError(t, repo.Create(ctx, op))
	}

	// Без AccountID-фильтра — все 3 строки.
	ops, _, err := repo.List(ctx, operations.ListFilter{PageSize: 100})
	require.NoError(t, err)
	assert.Len(t, ops, 3, "без AccountID-фильтра возвращаются все строки, включая NULL")
}

// TestRepo_List_AccountID_And_ResourceID — оба фильтра комбинируются через AND.
func TestRepo_List_AccountID_And_ResourceID(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := operations.NewRepo(pool, "public")

	// op1: resource_id=prj-Y, account_id=acc-X
	m1 := buildMetadata(t, "M1",
		[2]string{"project_id", "prj-Y"},
		[2]string{"account_id", "acc-X"},
	)
	op1, err := operations.New("iop", "op1", m1)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(ctx, op1, operations.SystemPrincipal()))

	// op2: resource_id=prj-Z, account_id=acc-X (другой ресурс того же account)
	m2 := buildMetadata(t, "M2",
		[2]string{"project_id", "prj-Z"},
		[2]string{"account_id", "acc-X"},
	)
	op2, err := operations.New("iop", "op2", m2)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(ctx, op2, operations.SystemPrincipal()))

	ops, _, err := repo.List(ctx, operations.ListFilter{
		AccountID:  "acc-X",
		ResourceID: "prj-Y",
		PageSize:   100,
	})
	require.NoError(t, err)
	require.Len(t, ops, 1)
	assert.Equal(t, "op1", ops[0].Description)
}
