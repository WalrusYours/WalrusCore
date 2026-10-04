package memory

import (
	"testing"

	"github.com/timurcravtov/walrus/internal/store"
	"github.com/timurcravtov/walrus/internal/store/storetest"
)

func TestInMemoryStore(t *testing.T) {
	storetest.Run(t, func() store.Store { return New() })
}
