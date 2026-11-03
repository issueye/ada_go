package browser

import "fmt"

// Error is a tool error with a stable code. The HTTP layer returns it to the
// agent, which can use Code and Hint to correct itself.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return e.Code + ": " + e.Message + " (" + e.Hint + ")"
	}
	return e.Code + ": " + e.Message
}

func NewError(code, msg string) *Error { return &Error{Code: code, Message: msg} }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Error codes shared with the MCP/CLI layer.
const (
	CodeBadRequest     = "bad_request"
	CodeInvalidTab     = "invalid_tab"
	CodeNoTab          = "no_tab"
	CodeTabClosed      = "tab_closed"
	CodeTooManyTabs    = "too_many_tabs"
	CodeProtectedTab   = "protected_tab"
	CodeStaleRef       = "stale_ref"
	CodeNotFound       = "not_found"
	CodeAmbiguous      = "ambiguous"
	CodeNotVisible     = "not_visible"
	CodeNotEnabled     = "not_enabled"
	CodeCovered        = "covered"
	CodeTimeout        = "timeout"
	CodeNavFailed      = "nav_failed"
	CodeBlockedURL     = "blocked_url"
	CodeBlockedAction  = "blocked_action"
	CodeControlLocked  = "control_locked"
	CodePageUnresp     = "page_unresponsive"
	CodeUnsupported    = "unsupported"
	CodeInternal       = "internal"
	CodeTooManyActions = "too_many_actions"

	CodeScriptSyntax      = "script_syntax"
	CodeScriptError       = "script_error"
	CodeScriptTimeout     = "script_timeout"
	CodeScriptMemory      = "script_memory"
	CodeScriptOutput      = "script_output_limit"
	CodeScriptResult      = "script_result_too_large"
	CodeScriptSource      = "script_source_too_large"
	CodeScriptActions     = "script_actions_limit"
	CodeScriptBusy        = "script_busy"
	CodeScriptHung        = "script_hung"
	CodeScriptUnsupported = "script_unsupported"
)
