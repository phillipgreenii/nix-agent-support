package orphan

import (
	"os"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
