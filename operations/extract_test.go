// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations

import (
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
	"google.golang.org/protobuf/types/known/anypb"
)

var extractTestFileSeq atomic.Int64

// buildAny строит синтетический *anypb.Any с указанными string-полями по их
// exact-именам (dynamicpb), без зависимости от kacho-iam proto-stubs. Порядок
// fields задает field-number 1..N (важно для non-first account_id-инварианта).
//
// Синтетический тип регистрируется в глобальном protoregistry, чтобы
// anypb.UnmarshalNew (глобальный resolver, как в продовом extractResourceID /
// extractAccountID) смог его разрезолвить.
func buildAny(t *testing.T, msgName string, fields ...[2]string) *anypb.Any {
	t.Helper()

	var fieldDescs []*descriptorpb.FieldDescriptorProto
	for i, f := range fields {
		num := int32(i + 1)
		name := f[0]
		fieldDescs = append(fieldDescs, &descriptorpb.FieldDescriptorProto{
			Name:   &name,
			Number: &num,
			Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		})
	}

	syntax := "proto3"
	// Уникальный package на каждый вызов — избегаем коллизий в глобальном registry.
	seq := extractTestFileSeq.Add(1)
	pkg := fmt.Sprintf("kacho.test.operations.extract.s%d", seq)
	mName := msgName
	fname := fmt.Sprintf("operations_extract_test_%s_%d.proto", msgName, seq)
	fdp := &descriptorpb.FileDescriptorProto{
		Name:        &fname,
		Package:     &pkg,
		Syntax:      &syntax,
		MessageType: []*descriptorpb.DescriptorProto{{Name: &mName, Field: fieldDescs}},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	require.NoError(t, err)

	md := fd.Messages().Get(0)
	require.NoError(t, protoregistry.GlobalFiles.RegisterFile(fd))
	require.NoError(t, protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(md)))

	dyn := dynamicpb.NewMessage(md)
	for _, f := range fields {
		fldDesc := md.Fields().ByName(protoreflect.Name(f[0]))
		require.NotNil(t, fldDesc)
		dyn.Set(fldDesc, protoreflect.ValueOfString(f[1]))
	}
	a, err := anypb.New(dyn)
	require.NoError(t, err)
	return a
}

// TestExtractAccountID_ExactName — extractAccountID читает поле строго по имени
// "account_id" и НЕ путает его с другими _id-полями.
func TestExtractAccountID_ExactName(t *testing.T) {
	tests := []struct {
		name   string
		fields [][2]string
		want   string
	}{
		{
			name:   "project_id first, account_id non-first",
			fields: [][2]string{{"project_id", "prj-Y"}, {"account_id", "acc-X"}},
			want:   "acc-X",
		},
		{
			name:   "user_id first, account_id non-first (InviteUserMetadata-like)",
			fields: [][2]string{{"user_id", "usr-W"}, {"account_id", "acc-X"}, {"magic_link_url", "u"}},
			want:   "acc-X",
		},
		{
			name:   "account_id is the only field (AccountMetadata-like)",
			fields: [][2]string{{"account_id", "acc-X"}},
			want:   "acc-X",
		},
		{
			name:   "no account_id field (VPC-like) → empty",
			fields: [][2]string{{"subnet_id", "snt-Q"}},
			want:   "",
		},
		{
			name:   "service_account_id present but no exact account_id → empty (no substring match)",
			fields: [][2]string{{"service_account_id", "sva-Z"}},
			want:   "",
		},
		{
			name:   "account_id present but empty value → empty",
			fields: [][2]string{{"project_id", "prj-Y"}, {"account_id", ""}},
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := buildAny(t, "M", tc.fields...)
			got := extractAccountID(meta)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestExtractAccountID_NilMetadata — nil metadata → "".
func TestExtractAccountID_NilMetadata(t *testing.T) {
	assert.Equal(t, "", extractAccountID(nil))
}

// TestExtractResourceID_UnchangedByAccountID — резервный guard: при наличии и
// первого _id-поля, и account_id, extractResourceID по-прежнему берет первое
// _id-поле (а не account_id): resource_id-путь не сломан.
func TestExtractResourceID_UnchangedByAccountID(t *testing.T) {
	meta := buildAny(t, "M",
		[2]string{"project_id", "prj-Y"},
		[2]string{"account_id", "acc-X"},
	)
	assert.Equal(t, "prj-Y", extractResourceID(meta),
		"extractResourceID должен оставаться первым _id-полем (project_id)")
}

// TestExtractResourceID_PrefersExplicitResourceIDField — если у metadata есть
// поле, названное РОВНО resource_id, extractResourceID берёт его, а НЕ первое
// попавшееся *_id-поле. Иначе denorm-колонка resource_id получала бы чужой id
// (напр. project_id, объявленный раньше owning resource_id), и List-фильтр по
// resource_id мис-атрибутировал бы операцию.
func TestExtractResourceID_PrefersExplicitResourceIDField(t *testing.T) {
	// project_id объявлено ПЕРВЫМ, resource_id — вторым. Reflection-fallback
	// «первое _id-поле» вернул бы project_id (баг мис-атрибуции).
	meta := buildAny(t, "IAMLikeMetadata",
		[2]string{"project_id", "prj-Y"},
		[2]string{"resource_id", "usr-real"},
	)
	assert.Equal(t, "usr-real", extractResourceID(meta),
		"явное поле resource_id должно побеждать первое _id-поле (project_id)")
}

// TestExtractResourceID_ResourceIDFieldEmptyFallsBack — если поле resource_id
// присутствует, но пустое, extractResourceID откатывается на первое непустое
// *_id-поле (back-compat: не теряем денорм-ключ из-за пустого явного поля).
func TestExtractResourceID_ResourceIDFieldEmptyFallsBack(t *testing.T) {
	meta := buildAny(t, "IAMLikeMetadataEmptyRID",
		[2]string{"network_id", "net-abc"},
		[2]string{"resource_id", ""},
	)
	assert.Equal(t, "net-abc", extractResourceID(meta),
		"пустое resource_id → fallback на первое непустое _id-поле")
}

// TestResolveResourceID_ExplicitWins — если use-case явно задал Operation.ResourceID,
// именно оно денормализуется в колонку resource_id, а НЕ угаданное reflection'ом
// первое _id-поле metadata. Это защищает от fragile «первое _id == owning resource»:
// метаданные, где первым объявлено НЕ-owning поле (folder_id/parent_id), больше не
// уводят resource_id на чужой id.
func TestResolveResourceID_ExplicitWins(t *testing.T) {
	// Метаданные, где ПЕРВЫМ идёт не-owning folder_id, а owning address_id — второй.
	meta := buildAny(t, "CreateAddressMetadata",
		[2]string{"folder_id", "prj-owner"},
		[2]string{"address_id", "adr-real"},
	)
	// Reflection взял бы folder_id (первое _id-поле) — это и есть баг.
	require.Equal(t, "prj-owner", extractResourceID(meta),
		"sanity: reflection берёт первое _id-поле (folder_id) — фрагильно")

	op := Operation{Metadata: meta, ResourceID: "adr-real"}
	assert.Equal(t, "adr-real", resolveResourceID(op),
		"явный Operation.ResourceID должен побеждать reflection-угадывание")
}

// TestResolveResourceID_ReflectionFallback — если явный ResourceID пуст, остаётся
// прежнее reflection-поведение (back-compat для существующих use-case'ов).
func TestResolveResourceID_ReflectionFallback(t *testing.T) {
	meta := buildAny(t, "CreateNetworkMetadata",
		[2]string{"network_id", "net-abc"},
	)
	op := Operation{Metadata: meta} // ResourceID не задан
	assert.Equal(t, "net-abc", resolveResourceID(op),
		"при пустом ResourceID — fallback на reflection (первое _id-поле)")
}
