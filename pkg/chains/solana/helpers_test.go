package solana

import (
	"os"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}
