module github.com/SirNiklas9/projx-engine/host

go 1.25.6

require (
	github.com/BananaLabs-OSS/Pulp v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-confine v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-egress v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-entropy v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-fs v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-http v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-process v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-pty v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-sqlite v0.0.0
)

require (
	github.com/BananaLabs-OSS/Pulp-ext-hook v0.0.0 // indirect
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/aymanbagabas/go-pty v0.2.3 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/landlock-lsm/go-landlock v0.9.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tetratelabs/wazero v1.11.0 // indirect
	github.com/u-root/u-root v0.16.0 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/exp v0.0.0-20260611194520-c48552f49976 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8 // indirect
	kernel.org/pub/linux/libs/security/libcap/psx v1.2.77 // indirect
	modernc.org/libc v1.70.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.48.2 // indirect
)

replace (
	github.com/BananaLabs-OSS/Pulp => ../../Pulp

	github.com/BananaLabs-OSS/Pulp-ext-acquire => ../../Pulp-ext-acquire
	github.com/BananaLabs-OSS/Pulp-ext-confine => ../../Pulp-ext-confine
	github.com/BananaLabs-OSS/Pulp-ext-egress => ../../Pulp-ext-egress
	github.com/BananaLabs-OSS/Pulp-ext-entropy => ../../Pulp-ext-entropy
	github.com/BananaLabs-OSS/Pulp-ext-fs => ../../Pulp-ext-fs
	github.com/BananaLabs-OSS/Pulp-ext-hook => ../../Pulp-ext-hook
	github.com/BananaLabs-OSS/Pulp-ext-http => ../../Pulp-ext-http
	github.com/BananaLabs-OSS/Pulp-ext-mdns => ../../Pulp-ext-mdns
	github.com/BananaLabs-OSS/Pulp-ext-process => ../../Pulp-ext-process
	github.com/BananaLabs-OSS/Pulp-ext-pty => ../../Pulp-ext-pty
	github.com/BananaLabs-OSS/Pulp-ext-sqlite => ../../Pulp-ext-sqlite
	github.com/BananaLabs-OSS/Pulp-ext-toolchain => ../../Pulp-ext-toolchain
	github.com/BananaLabs-OSS/Pulp-ext-udp => ../../Pulp-ext-udp
	github.com/BananaLabs-OSS/Pulp-ext-wsout => ../../Pulp-ext-wsout
)
