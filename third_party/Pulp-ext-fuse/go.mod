module github.com/BananaLabs-OSS/Pulp-ext-fuse

go 1.25.6

require (
	github.com/BananaLabs-OSS/Pulp v0.0.0
	github.com/BananaLabs-OSS/Pulp-grants v0.0.0
	github.com/hanwen/go-fuse/v2 v2.7.2
	github.com/tetratelabs/wazero v1.11.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	modernc.org/libc v1.70.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.48.2 // indirect
)

replace github.com/BananaLabs-OSS/Pulp => ../Pulp

replace github.com/BananaLabs-OSS/Pulp-grants => ../Pulp-grants
