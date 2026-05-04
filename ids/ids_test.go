package ids

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewID_Length(t *testing.T) {
	id := NewID(PrefixNetwork)
	require.Len(t, id, 20)
}

func TestNewID_PrefixApplied(t *testing.T) {
	id := NewID(PrefixCloud)
	require.True(t, strings.HasPrefix(id, PrefixCloud), "id %q must start with %q", id, PrefixCloud)
}

func TestNewID_BodyIsCrockfordBase32(t *testing.T) {
	id := NewID(PrefixSubnet)
	body := id[3:]
	require.Len(t, body, 17)
	for i := 0; i < len(body); i++ {
		require.True(t, isCrockfordChar(body[i]),
			"body[%d]=%q is not crockford-base32 char (id=%q)", i, body[i], id)
	}
}

func TestNewID_Unique(t *testing.T) {
	seen := make(map[string]bool, 10000)
	for i := 0; i < 10000; i++ {
		id := NewID(PrefixNetwork)
		require.False(t, seen[id], "duplicate id %q at iter %d", id, i)
		seen[id] = true
	}
}

func TestNewID_PanicsOnBadPrefix(t *testing.T) {
	require.Panics(t, func() { NewID("ab") })
	require.Panics(t, func() { NewID("abcd") })
	require.Panics(t, func() { NewID("") })
}

func TestIsValid_OK(t *testing.T) {
	id := NewID(PrefixNetwork)
	require.True(t, IsValid(id, PrefixNetwork))
}

func TestIsValid_WrongPrefix(t *testing.T) {
	id := NewID(PrefixNetwork)
	require.False(t, IsValid(id, PrefixSubnet))
}

func TestIsValid_WrongLength(t *testing.T) {
	require.False(t, IsValid("enp123", PrefixNetwork))
	require.False(t, IsValid("enp"+strings.Repeat("a", 18), PrefixNetwork))
}

func TestIsValid_BadChars(t *testing.T) {
	// I, L, O, U — запрещены crockford
	require.False(t, IsValid("enp"+strings.Repeat("i", 17), PrefixNetwork))
	require.False(t, IsValid("enp"+strings.Repeat("l", 17), PrefixNetwork))
	require.False(t, IsValid("enp"+strings.Repeat("o", 17), PrefixNetwork))
	require.False(t, IsValid("enp"+strings.Repeat("u", 17), PrefixNetwork))
	// uppercase запрещён в нашей нормализации
	require.False(t, IsValid("ENP"+strings.Repeat("a", 17), PrefixNetwork))
}

func TestHasKnownPrefix_AcceptsValid(t *testing.T) {
	for _, p := range []string{
		PrefixCloud, PrefixFolder, PrefixOrganization,
		PrefixNetwork, PrefixSubnet, PrefixAddress,
		PrefixRouteTable, PrefixSecurityGroup,
	} {
		id := NewID(p)
		require.True(t, HasKnownPrefix(id), "id=%q (prefix=%q)", id, p)
	}
}

func TestHasKnownPrefix_RejectsBadShape(t *testing.T) {
	require.False(t, HasKnownPrefix("short"))
	require.False(t, HasKnownPrefix("enp_with_underscore"))
	require.False(t, HasKnownPrefix(""))
}

func TestNewUID_LegacyShapeStable(t *testing.T) {
	uid := NewUID()
	require.Len(t, uid, 20)
	require.True(t, strings.HasPrefix(uid, "rev"), "legacy NewUID has rev-prefix sentinel")
}
