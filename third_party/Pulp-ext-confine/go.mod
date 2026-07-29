module github.com/BananaLabs-OSS/Pulp-ext-confine

go 1.25.6

require (
	github.com/BananaLabs-OSS/Pulp v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-egress v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-hook v0.0.0
	github.com/landlock-lsm/go-landlock v0.9.0
	github.com/tetratelabs/wazero v1.11.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
	golang.org/x/sys v0.46.0
)

require (
	github.com/google/btree v1.1.3 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/exp v0.0.0-20260611194520-c48552f49976 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8 // indirect
	kernel.org/pub/linux/libs/security/libcap/psx v1.2.77 // indirect
)

replace github.com/BananaLabs-OSS/Pulp => ../Pulp

replace github.com/BananaLabs-OSS/Pulp-ext-egress => ../Pulp-ext-egress

replace github.com/BananaLabs-OSS/Pulp-ext-hook => ../Pulp-ext-hook
