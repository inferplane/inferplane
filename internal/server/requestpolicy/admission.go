package requestpolicy

import (
	"context"
	"net/http"

	"github.com/inferplane/inferplane/internal/governance"
)

type admissionKey struct{}

func WithAdmission(req *http.Request, a *governance.Admission) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), admissionKey{}, a))
}

func AdmissionFrom(req *http.Request) *governance.Admission {
	a, _ := req.Context().Value(admissionKey{}).(*governance.Admission)
	return a
}
