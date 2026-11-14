module github.com/mhenderik/sendora

go 1.26.0

require (
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/mattn/go-sqlite3 v1.14.53
	golang.org/x/crypto v0.58.0
	golang.org/x/oauth2 v0.38.0
)

require github.com/go-jose/go-jose/v4 v4.1.4 // indirect

replace github.com/mattn/go-sqlite3 => ./third_party/go-sqlite3
