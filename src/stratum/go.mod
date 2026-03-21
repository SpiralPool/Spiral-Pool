module github.com/spiralpool/stratum

go 1.26.1

require (
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.3.0
	github.com/go-zeromq/zmq4 v0.17.0
	github.com/jackc/pgx/v5 v5.7.2
	github.com/lib/pq v1.10.9
	github.com/prometheus/client_golang v1.20.5
	github.com/prometheus/client_model v0.6.1
	github.com/redis/go-redis/v9 v9.17.2
	go.uber.org/zap v1.27.0
	golang.org/x/crypto v0.46.0
	golang.org/x/sys v0.39.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/go-zeromq/goczmq/v4 v4.2.2 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)

// ZMQ provides instant block notifications from DigiByte Core
// Uses go-zeromq (pure Go implementation, no C dependencies required)
