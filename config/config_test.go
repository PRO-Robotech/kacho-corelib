// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type fooConfig struct {
	GrpcPort int    `envconfig:"KACHO_FOO_GRPC_PORT" required:"true"`
	DBDsn    string `envconfig:"KACHO_FOO_DB_DSN" required:"true"`
}

func TestLoad_FillsFromEnv(t *testing.T) {
	t.Setenv("KACHO_FOO_GRPC_PORT", "9090")
	t.Setenv("KACHO_FOO_DB_DSN", "postgres://x")

	var c fooConfig
	require.NoError(t, Load(&c))
	require.Equal(t, 9090, c.GrpcPort)
	require.Equal(t, "postgres://x", c.DBDsn)
}

func TestLoad_FailsOnMissingRequired(t *testing.T) {
	var c fooConfig
	err := Load(&c)
	require.Error(t, err)
	require.Contains(t, err.Error(), "KACHO_FOO_GRPC_PORT")
}
