package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/ogen-go/ogen/validate"

	"sandboxd/internal/cp"
	"sandboxd/internal/gen/adminapi"
	"sandboxd/internal/gen/clientapi"
)

// The one place in the control plane that knows a status code. Use cases return the
// sentinels in cp/errors.go, the generated servers return the types in `ogenerrors`, and
// this file turns either into `{error, issues?}`.
//
// A generated server reaches it two ways, and both are wired in `New`. A handler's error
// and a rejected credential go through `NewError`, which is typed against one document's
// `ErrorModel`; a request that failed to decode goes through the server's `ErrorHandler`,
// which is handed a ResponseWriter instead. `fault` is the shape in between, so the
// mapping is written once and the two documents' models are two conversions of it.

// fault is a failure before it is typed into a document's own model.
type fault struct {
	status  int
	message string
	issues  []flaw
}

// flaw names one field a caller has to fix, as they sent it.
type flaw struct {
	path    string
	message string
}

func (f flaw) sentence() string {
	if f.path == "" || strings.HasPrefix(f.message, f.path) {
		return f.message
	}
	return f.path + ": " + f.message
}

// problem is the whole mapping. The order is the point: our own sentinels first, because a
// use case's answer is the most specific thing anyone knows about a request, and the
// generated errors after, because they describe a request that never reached one.
func problem(log *slog.Logger, err error) fault {
	switch {
	case errors.Is(err, cp.ErrNotFound):
		return fault{status: http.StatusNotFound, message: err.Error()}
	case errors.Is(err, cp.ErrConflict):
		return fault{status: http.StatusConflict, message: err.Error()}
	case errors.Is(err, cp.ErrInvalid):
		return fault{status: http.StatusBadRequest, message: err.Error()}
	case errors.Is(err, cp.ErrUnsatisfiable):
		// Not a 400: the request is well formed, the fleet just cannot ever serve it.
		return fault{status: http.StatusUnprocessableEntity, message: err.Error()}
	}

	// A missing token and a wrong one get the same answer, and neither gets a reason: the
	// only thing a caller can do about either is present a different token.
	var security *ogenerrors.SecurityError
	if errors.As(err, &security) {
		return fault{status: http.StatusUnauthorized, message: errBadToken.Error()}
	}

	// The body did not validate. ogen reports every failing field at once, in a struct, so
	// the caller sees all of them in one round trip.
	var invalid *validate.Error
	if errors.As(err, &invalid) {
		issues := make([]flaw, 0, len(invalid.Fields))
		for _, f := range invalid.Fields {
			issues = append(issues, flaw{path: f.Name, message: innermost(f.Error).Error()})
		}
		return badRequest(issues)
	}
	// A parameter did not: one at a time, because the decoder stops at the first.
	var param *ogenerrors.DecodeParamError
	if errors.As(err, &param) {
		return badRequest([]flaw{{path: param.Name, message: innermost(param.Err).Error()}})
	}

	// Everything else ogen raises: a malformed body, an unknown field, the wrong content
	// type. Each carries its own status, and the sentence a caller can act on is the
	// innermost one — the layers above it name an operation they already know they called.
	var ogenErr ogenerrors.Error
	if errors.As(err, &ogenErr) {
		return fault{status: ogenErr.Code(), message: innermost(err).Error()}
	}

	// Anything unrecognised is a bug in this process and says nothing beyond "internal".
	log.Error("unhandled", "err", err)
	return fault{status: http.StatusInternalServerError, message: "internal error"}
}

// badRequest names each thing a caller has to fix. The prose is the first problem, so a
// client with no interest in `issues` still has something to show.
func badRequest(issues []flaw) fault {
	if len(issues) == 0 {
		return fault{status: http.StatusBadRequest, message: "invalid request"}
	}
	return fault{
		status:  http.StatusBadRequest,
		message: issues[0].sentence(),
		issues:  issues,
	}
}

// innermost is the sentence at the bottom of a wrapped error. ogen wraps generously —
// `operation CreateSandbox: decode request: decode application/json: decode CreateSandbox:
// unexpected field "repo"` — and only the last clause is news to the caller.
func innermost(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// --- the two documents' models ---------------------------------------------------------

func clientErrors(log *slog.Logger) clientapi.ErrorHandler {
	return func(_ context.Context, w http.ResponseWriter, _ *http.Request, err error) {
		write(w, problem(log, err))
	}
}

func adminErrors(log *slog.Logger) adminapi.ErrorHandler {
	return func(_ context.Context, w http.ResponseWriter, _ *http.Request, err error) {
		write(w, problem(log, err))
	}
}

func (h *sandboxHandler) NewError(_ context.Context, err error) *clientapi.ErrorStatusCode {
	f := problem(h.log, err)
	out := &clientapi.ErrorStatusCode{
		StatusCode: f.status,
		Response:   clientapi.ErrorModel{Error: f.message},
	}
	for _, i := range f.issues {
		out.Response.Issues = append(out.Response.Issues, clientapi.Issue{
			Path: clientapi.NewOptString(i.path), Message: i.message,
		})
	}
	return out
}

func (h *hostHandler) NewError(_ context.Context, err error) *adminapi.ErrorStatusCode {
	f := problem(h.log, err)
	out := &adminapi.ErrorStatusCode{
		StatusCode: f.status,
		Response:   adminapi.ErrorModel{Error: f.message},
	}
	for _, i := range f.issues {
		out.Response.Issues = append(out.Response.Issues, adminapi.Issue{
			Path: adminapi.NewOptString(i.path), Message: i.message,
		})
	}
	return out
}

// write is the ErrorHandler path, where there is a ResponseWriter and no generated
// encoder. The shape is `ErrorModel`, which both documents declare identically — that they
// must agree is a fact about this function, not a dependency between the contracts.
func write(w http.ResponseWriter, f fault) {
	body := struct {
		Error  string `json:"error"`
		Issues []flaw `json:"issues,omitempty"`
	}{Error: f.message, Issues: f.issues}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	_ = json.NewEncoder(w).Encode(body)
}

// MarshalJSON is here because `flaw` is unexported and its fields are too: `write` is the
// only encoder that sees one, and giving the struct tags to the fields instead would make
// them look like part of an API they are not.
func (f flaw) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Path    string `json:"path,omitempty"`
		Message string `json:"message"`
	}{Path: f.path, Message: f.message})
}
