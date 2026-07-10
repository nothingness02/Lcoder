package tools

import "errors"

// ErrToolExecution is a sentinel error that tools can return to indicate a
// business-level failure (e.g. a shell command exited non-zero). The registry
// surfaces the result to the LLM as a normal tool outcome rather than a
// framework/system error.
//
// Tools are free to return (result, nil) instead; this sentinel exists for cases
// where an error value is still useful for control flow but should not be
// reported as a system error to the model.
var ErrToolExecution = errors.New("tool execution failed")
