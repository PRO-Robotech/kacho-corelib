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
// fields задаёт field-number 1..N (важно для non-first account_id-инварианта).
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
// "account_id" и НЕ путает его с другими _id-полями. Verifies D-5 (2)/(3),
// acceptance 1.2 (d).
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
// первого _id-поля, и account_id, extractResourceID по-прежнему берёт первое
// _id-поле (а не account_id). Verifies D-5 (2): resource_id-путь не сломан.
func TestExtractResourceID_UnchangedByAccountID(t *testing.T) {
	meta := buildAny(t, "M",
		[2]string{"project_id", "prj-Y"},
		[2]string{"account_id", "acc-X"},
	)
	assert.Equal(t, "prj-Y", extractResourceID(meta),
		"extractResourceID должен оставаться первым _id-полем (project_id)")
}
