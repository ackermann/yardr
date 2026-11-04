package server

import (
	"os"
	"testing"

	"yardr.dev/yardr/internal/scripttest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	scripttest.Remove()
	os.Exit(code)
}
