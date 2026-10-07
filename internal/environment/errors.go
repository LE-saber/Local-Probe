package environment

// ErrorCode is a stable, path-free category for discovery failures.
type ErrorCode string

const (
	CodeInvalidInput      ErrorCode = "invalid_input"
	CodeDuplicateID       ErrorCode = "duplicate_logical_id"
	CodeRejectedCandidate ErrorCode = "rejected_candidate"
	CodeUnavailable       ErrorCode = "unavailable"
	CodeCandidateLimit    ErrorCode = "candidate_limit"
)

// Error intentionally contains no path, username, or operating-system error
// text so it can safely cross an API boundary.
type Error struct {
	Code ErrorCode
}

func (e *Error) Error() string { return string(e.Code) }

func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e != nil && other != nil && e.Code == other.Code
}

var (
	ErrInvalidInput      = &Error{Code: CodeInvalidInput}
	ErrDuplicateID       = &Error{Code: CodeDuplicateID}
	ErrRejectedCandidate = &Error{Code: CodeRejectedCandidate}
	ErrUnavailable       = &Error{Code: CodeUnavailable}
	ErrCandidateLimit    = &Error{Code: CodeCandidateLimit}
)
