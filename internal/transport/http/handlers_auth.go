package http

import "net/http"

// authHandler serves the Authentication tag. CloudOptix does not issue its
// own passwords or sessions — an operator authenticates against the tenant's
// OIDC provider (or presents a service/API-key credential for machine
// clients) and this API only ever validates what it is handed, per
// internal/infrastructure/auth. The one endpoint that genuinely belongs to
// this transport layer, then, is "who does the server think I am": it
// reflects back exactly what the authentication middleware resolved, which
// is what a client uses to confirm its token, tenant header and role
// mapping actually landed the way it expected before relying on them.
type authHandler struct{}

type whoamiResponse struct {
	Subject    string   `json:"subject"`
	TenantID   string   `json:"tenant_id"`
	Email      string   `json:"email,omitempty"`
	Name       string   `json:"name,omitempty"`
	Roles      []string `json:"roles"`
	Machine    bool     `json:"machine"`
	AuthMethod string   `json:"auth_method"`
}

func (authHandler) WhoAmI(w http.ResponseWriter, r *http.Request) {
	p := MustPrincipal(r.Context())
	roles := make([]string, 0, len(p.Roles))
	for _, role := range p.Roles {
		roles = append(roles, string(role))
	}
	WriteJSON(w, http.StatusOK, whoamiResponse{
		Subject: p.Subject, TenantID: string(p.TenantID), Email: p.Email, Name: p.Name,
		Roles: roles, Machine: p.Machine, AuthMethod: AuthMethodFrom(r.Context()),
	})
}
