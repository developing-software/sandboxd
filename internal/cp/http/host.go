package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"sandboxd/internal/cp"
)

// Worker hosts: list, approve, revoke.

type (
	hostIn struct {
		ID string `path:"id"`
	}
	approveIn struct {
		ID   string `path:"id"`
		Body approveBody
	}
	approveBody struct {
		Code string `json:"code" minLength:"1" doc:"What the worker printed when it enrolled."`
	}

	hostListOut struct {
		Body []cp.HostView
	}
	okOut struct {
		Body ok
	}
	ok struct {
		OK bool `json:"ok" enum:"true"`
	}
)

func registerHosts(api huma.API, h hostAPI, log *slog.Logger) {
	tag := []string{"Hosts"}

	huma.Register(api, huma.Operation{
		OperationID: "list-hosts",
		Method:      http.MethodGet,
		Path:        "/hosts",
		Summary:     "List hosts",
		Description: "Every enrolled host, with what it reported about itself.",
		Tags:        tag,
		Errors:      []int{http.StatusUnauthorized},
	}, func(_ context.Context, _ *struct{}) (*hostListOut, error) {
		v, err := h.List()
		if err != nil {
			return nil, fail(log, err)
		}
		return &hostListOut{Body: v}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "approve-host",
		Method:      http.MethodPost,
		Path:        "/hosts/{id}/approve",
		Summary:     "Approve a pending host",
		Description: "A worker started with the operator's join token skips this and is approved on hello.",
		Tags:        tag,
		Errors: []int{
			http.StatusBadRequest, http.StatusUnauthorized,
			http.StatusNotFound, http.StatusConflict,
		},
	}, func(_ context.Context, in *approveIn) (*okOut, error) {
		if err := h.Approve(in.ID, in.Body.Code); err != nil {
			return nil, fail(log, err)
		}
		return &okOut{Body: ok{OK: true}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revoke-host",
		Method:      http.MethodPost,
		Path:        "/hosts/{id}/revoke",
		Summary:     "Revoke a host",
		Description: "The host is rejected on its next hello, and on the socket it is holding now.",
		Tags:        tag,
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound},
	}, func(_ context.Context, in *hostIn) (*okOut, error) {
		if err := h.Revoke(in.ID); err != nil {
			return nil, fail(log, err)
		}
		return &okOut{Body: ok{OK: true}}, nil
	})
}
