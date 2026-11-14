module github.com/thehaymaker/kagent-porcelain/server

go 1.27.3

// Substrate is published under a vanity path that resolves to kagent's fork;
// kagent's own go.mod carries the same replacement, and Go does not inherit it.
replace go.ate.dev/substrate => github.com/kagent-dev/substrate v1.0.0

require (
	github.com/a2aproject/a2a-go/v2 v2.7.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/kagent-dev/kagent/go v0.0.0-20261113212255-eb42f649dfae
	github.com/lestrrat-go/jwx v1.2.31
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260825204119-511051f7f437.2 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/goccy/go-json v0.11.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/lestrrat-go/backoff/v2 v2.0.8 // indirect
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/iter v1.0.2 // indirect
	github.com/lestrrat-go/option v1.0.1 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pressly/goose/v3 v3.29.0 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	go.ate.dev/substrate v1.0.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261005182115-fad411399dd8 // indirect
)
