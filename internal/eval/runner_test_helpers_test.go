package eval

import "errors"

// errCheckFake is a sentinel replay error for tests.
var errCheckFake = errors.New("replay failed")
