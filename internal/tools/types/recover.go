package types

import (
	"fmt"
	"log"
	"runtime/debug"
)

// RecoverPanic is deferred at the top of goroutines that tools spawn
// themselves (parallel searches, fan-out). Registry.ExecuteTool recovers
// panics on the calling goroutine only; a panic in a tool-spawned goroutine
// would otherwise crash the whole gateway (conduit-31jg.73).
//
// It logs the panic with a stack trace and, when onPanic is non-nil, hands
// it a descriptive error so the goroutine's result slot records a failure:
//
//	go func() {
//		defer wg.Done()
//		defer types.RecoverPanic("FTS search", func(err error) { ftsErr = err })
//		ftsResults, ftsErr = search()
//	}()
//
// It must be deferred directly (recover only works in the deferred call).
func RecoverPanic(what string, onPanic func(err error)) {
	rec := recover()
	if rec == nil {
		return
	}
	log.Printf("[Tools] PANIC in %s: %v\n%s", what, rec, debug.Stack())
	if onPanic != nil {
		onPanic(fmt.Errorf("%s panicked: %v", what, rec))
	}
}
