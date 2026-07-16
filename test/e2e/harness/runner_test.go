package harness_test

import (
	"testing"

	"shopflow/test/e2e/harness"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHarnessConfiguration(t *testing.T) {
	cfg := harness.LoadConfig()
	require.NotNil(t, cfg)
	assert.NotEmpty(t, cfg.BaseURL)
	assert.NotEmpty(t, cfg.DatabaseURL)
	assert.NotEmpty(t, cfg.KafkaBrokers)
}

func TestHarnessEnvironmentSetup(t *testing.T) {
	env := harness.SetupEnvironment(t)
	require.NotNil(t, env)
	assert.NotNil(t, env.Cfg)
	assert.NotNil(t, env.Client)
}
