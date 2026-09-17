package api

import (
	"log/slog"
	"net/http"
)

// rank orders the roles. A role reaches everything its rank allows and nothing
// above it, so adding a route means choosing the lowest role that should have
// it rather than listing who may call it.
var rank = map[Role]int{
	RoleReadOnly: 1,
	RoleOperator: 2,
	RoleAdmin:    3,
}

// allows reports whether a role reaches something needing at least minimum.
//
// An unknown role reaches nothing. A client certificate signed by the operator
// CA but carrying no recognised organisation has been authenticated and not
// authorised, and the two are different questions: somebody issuing a
// certificate without a role has not thereby granted every role.
func allows(held, minimum Role) bool {
	return rank[held] >= rank[minimum] && rank[held] > 0
}

// require wraps a handler in the lowest role that may call it.
//
// The check is here rather than inside each handler so that a route cannot be
// added without answering the question. A handler that forgets to authorise is
// a handler that authorises everybody, and the failure is invisible until
// somebody uses it.
func require(minimum Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		held := roleOf(r)

		if !allows(held, minimum) {
			// The client is authenticated -- TLS would have refused it
			// otherwise -- so this is 403 and not 401. Retrying with the same
			// certificate will never work, and saying so saves somebody
			// debugging their connection instead of their certificate.
			slog.Warn("refused an authenticated request",
				"path", r.URL.Path, "held", held, "needs", minimum)

			writeError(w, http.StatusForbidden, refusal(held, minimum))

			return
		}

		next(w, r)
	}
}

func refusal(held, minimum Role) string {
	if held == "" {
		return "your certificate carries no Corium role; reissue it with " +
			"`cctl pki issue --role " + short(minimum) + "`"
	}

	return "this needs " + string(minimum) + " and your certificate carries " + string(held)
}

// short is the role name as cctl takes it on the command line.
func short(role Role) string {
	switch role {
	case RoleReadOnly:
		return "readonly"
	case RoleOperator:
		return "operator"
	case RoleAdmin:
		return "admin"
	default:
		return string(role)
	}
}
