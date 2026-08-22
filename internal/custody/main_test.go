package custody

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	os.Unsetenv("GIT_CONFIG_COUNT")
	os.Exit(m.Run())
}
