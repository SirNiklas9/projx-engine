module github.com/BananaLabs-OSS/Pulp-cage

go 1.25.6

require (
	github.com/BananaLabs-OSS/Pulp-ext-confine v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-egress v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-fuse v0.0.0
	github.com/BananaLabs-OSS/Pulp-grants v0.0.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hanwen/go-fuse/v2 v2.7.2 // indirect
	github.com/landlock-lsm/go-landlock v0.9.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
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

replace github.com/BananaLabs-OSS/Pulp => ../Pulp

replace github.com/BananaLabs-OSS/Pulp-ext-confine => ../Pulp-ext-confine

replace github.com/BananaLabs-OSS/Pulp-ext-egress => ../Pulp-ext-egress

replace github.com/BananaLabs-OSS/Pulp-ext-fuse => ../Pulp-ext-fuse

replace github.com/BananaLabs-OSS/Pulp-grants => ../Pulp-grants
