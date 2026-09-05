package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"sandboxd/internal/cp"
)

// POST/GET/DELETE /sandboxes and the two token mints.

type (
	createIn struct {
		Body cp.CreateSandbox
	}
	listIn struct {
		OwnerID string `query:"owner_id" doc:"Restrict the list to one owner."`
	}
	ownedIn struct {
		ID      string `path:"id"`
		OwnerID string `query:"owner_id" required:"true"`
	}
	attachIn struct {
		ID   string `path:"id"`
		Body ownerBody
	}
	previewIn struct {
		ID   string `path:"id"`
		Body previewBody
	}

	ownerBody struct {
		OwnerID string `json:"owner_id" minLength:"1"`
	}
	previewBody struct {
		OwnerID string `json:"owner_id" minLength:"1"`
		Port    int    `json:"port" minimum:"1" maximum:"65535"`
	}

	sandboxOut struct {
		Body cp.SandboxView
	}
	sandboxListOut struct {
		Body []cp.SandboxView
	}
	attachOut struct {
		Body cp.AttachToken
	}
	previewOut struct {
		Body cp.PreviewToken
	}
)

func registerSandboxes(api huma.API, s sandboxAPI, log *slog.Logger) {
	tag := []string{"Sandboxes"}
	owned := []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound}

	huma.Register(api, huma.Operation{
		OperationID:   "create-sandbox",
		Method:        http.MethodPost,
		Path:          "/sandboxes",
		Summary:       "Create a sandbox",
		Description:   "Presets are resolved before this call, by the client: the body names an image, a command, env and the host tags it needs.",
		Tags:          tag,
		DefaultStatus: http.StatusCreated,
		Errors: []int{
			http.StatusBadRequest, http.StatusUnauthorized, http.StatusUnprocessableEntity,
		},
	}, func(ctx context.Context, in *createIn) (*sandboxOut, error) {
		v, err := s.Create(ctx, in.Body)
		if err != nil {
			return nil, fail(log, err)
		}
		return &sandboxOut{Body: v}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-sandboxes",
		Method:      http.MethodGet,
		Path:        "/sandboxes",
		Summary:     "List sandboxes",
		Description: "Newest first.",
		Tags:        tag,
		Errors:      []int{http.StatusUnauthorized},
	}, func(_ context.Context, in *listIn) (*sandboxListOut, error) {
		v, err := s.List(in.OwnerID)
		if err != nil {
			return nil, fail(log, err)
		}
		return &sandboxListOut{Body: v}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-sandbox",
		Method:      http.MethodGet,
		Path:        "/sandboxes/{id}",
		Summary:     "Get a sandbox",
		Tags:        tag,
		Errors:      owned,
	}, func(_ context.Context, in *ownedIn) (*sandboxOut, error) {
		v, err := s.Get(in.ID, in.OwnerID)
		if err != nil {
			return nil, fail(log, err)
		}
		return &sandboxOut{Body: v}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "end-sandbox",
		Method:      http.MethodDelete,
		Path:        "/sandboxes/{id}",
		Summary:     "End a sandbox",
		Description: "Returns the sandbox as it stands: ended, or ending while its host is told.",
		Tags:        tag,
		Errors:      owned,
	}, func(ctx context.Context, in *ownedIn) (*sandboxOut, error) {
		v, err := s.Cancel(ctx, in.ID, in.OwnerID)
		if err != nil {
			return nil, fail(log, err)
		}
		return &sandboxOut{Body: v}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "mint-attach-token",
		Method:      http.MethodPost,
		Path:        "/sandboxes/{id}/attach-token",
		Summary:     "Mint an attach token",
		Description: "Good for 60 s. Open wss_url from the browser.",
		Tags:        tag,
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict},
	}, func(_ context.Context, in *attachIn) (*attachOut, error) {
		v, err := s.AttachToken(in.ID, in.Body.OwnerID)
		if err != nil {
			return nil, fail(log, err)
		}
		return &attachOut{Body: v}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "mint-preview-token",
		Method:      http.MethodPost,
		Path:        "/sandboxes/{id}/preview-token",
		Summary:     "Mint a preview token",
		Description: "Good for 10 min. Open url from the browser; it sets the preview cookie and redirects.",
		Tags:        tag,
		Errors:      owned,
	}, func(_ context.Context, in *previewIn) (*previewOut, error) {
		v, err := s.PreviewToken(in.ID, in.Body.OwnerID, in.Body.Port)
		if err != nil {
			return nil, fail(log, err)
		}
		return &previewOut{Body: v}, nil
	})
}
