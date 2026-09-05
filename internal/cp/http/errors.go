package http

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/validation"

	"sandboxd/internal/cp"
)

// The one place in the control plane that knows a status code. Use cases return the
// sentinels in cp/errors.go and this file turns them into responses.

// errorModel is the API's single error shape: {error, issues?}. It replaces huma's RFC
// 9457 model, and because huma renders the document from whatever NewError returns, the
// OpenAPI document follows it without a second declaration.
type errorModel struct {
	status  int
	Message string  `json:"error" doc:"The first problem, in prose."`
	Issues  []issue `json:"issues,omitempty" doc:"Every failing field, for validation errors."`
}

type issue struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func (e *errorModel) Error() string  { return e.Message }
func (e *errorModel) GetStatus() int { return e.status }

var once sync.Once

// useErrorModel replaces huma's error constructor. It is process-wide state, so it is set
// once and never varies: every API in this binary speaks the same error shape.
func useErrorModel() {
	once.Do(func() {
		huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
			m := &errorModel{status: status, Message: msg}
			for _, err := range errs {
				var detail *huma.ErrorDetail
				switch {
				case errors.As(err, &detail):
					m.Issues = append(m.Issues, issue{
						Path: pathOf(detail), Message: detail.Message,
					})
				case err != nil:
					m.Issues = append(m.Issues, issue{Message: err.Error()})
				}
			}
			// huma reports a failed body or query validation as 422. The contract says a
			// malformed request is a 400, and keeps 422 for the one case that is not
			// malformed: tags no approved host can satisfy, which carries no field details.
			if status == http.StatusUnprocessableEntity && len(m.Issues) > 0 {
				m.status = http.StatusBadRequest
			}
			// The prose is the first problem, so a client with no interest in `issues`
			// still gets something to show.
			if len(m.Issues) > 0 {
				m.Message = m.Issues[0].sentence()
			}
			return m
		}
	})
}

func (i issue) sentence() string {
	if i.Path == "" || strings.HasPrefix(i.Message, i.Path) {
		return i.Message
	}
	return i.Path + ": " + i.Message
}

// missingProperty matches huma's own message for an absent required field, built from its
// format string rather than a copy of the prose.
var missingProperty = regexp.MustCompile("^" + strings.Replace(
	regexp.QuoteMeta(validation.MsgExpectedRequiredProperty), `%s`, `(\S+)`, 1) + "$")

// pathOf names the field a caller has to fix. huma reports most failures against the
// field itself, but an absent required property against the body as a whole — and the
// contract here is one issue per failing field, so the name is lifted back out.
func pathOf(detail *huma.ErrorDetail) string {
	if m := missingProperty.FindStringSubmatch(detail.Message); m != nil {
		return prefixed(field(detail.Location), m[1])
	}
	return field(detail.Location)
}

// prefixed joins a nested location back onto the property name, so a failure inside a
// nested object still reads as a path.
func prefixed(location, name string) string {
	if location == "" {
		return name
	}
	return location + "." + name
}

// field trims huma's location down to the field a caller sent: `body.owner_id` is
// `owner_id` to someone who only knows the request body.
func field(location string) string {
	for _, prefix := range []string{"body.", "query.", "path.", "header."} {
		if rest, ok := strings.CutPrefix(location, prefix); ok {
			return rest
		}
	}
	if location == "body" {
		return ""
	}
	return location
}

// fail maps a use case's error onto a status. Anything unrecognised is a bug in this
// process and says nothing beyond "internal".
func fail(log *slog.Logger, err error) error {
	switch {
	case errors.Is(err, cp.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, cp.ErrConflict):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, cp.ErrInvalid):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, cp.ErrUnsatisfiable):
		return huma.Error422UnprocessableEntity(err.Error())
	}
	log.Error("unhandled", "err", err)
	return huma.Error500InternalServerError("internal error")
}
