package tracera

import (
	"sync"

	"github.com/tracera-dev/tracera-go/internal/core"
)

// TestContext is the opaque customer pin — bind, section, and result fields
// are not reachable from test code (plugin runners use package plugin).
type TestContext struct {
	inner *core.TestContext
}

var pinCache sync.Map // *core.TestContext -> *TestContext

func wrapPin(c *core.TestContext) *TestContext {
	if c == nil {
		return nil
	}
	if v, ok := pinCache.Load(c); ok {
		return v.(*TestContext)
	}
	w := &TestContext{inner: c}
	actual, _ := pinCache.LoadOrStore(c, w)
	return actual.(*TestContext)
}

func unwrapPin(c *TestContext) *core.TestContext {
	if c == nil {
		return nil
	}
	return c.inner
}
